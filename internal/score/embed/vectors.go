// SPDX-License-Identifier: GPL-3.0-or-later

package embed

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Vectors is a word to vector lookup.
//
// The format is the one word2vec and GloVe both write and every tool reads: a
// word, then its numbers, one word per line, whitespace separated. Choosing
// the format everybody already has is what lets an operator point scour at a
// file they downloaded rather than one scour had to invent.
type Vectors struct {
	dim   int
	words map[string][]float32
}

// Len is how many words are loaded.
func (v *Vectors) Len() int {
	if v == nil {
		return 0
	}
	return len(v.words)
}

// Dim is the vector width.
func (v *Vectors) Dim() int {
	if v == nil {
		return 0
	}
	return v.dim
}

// Vector returns one word's vector.
func (v *Vectors) Vector(word string) ([]float32, bool) {
	if v == nil {
		return nil, false
	}
	vec, ok := v.words[strings.ToLower(word)]
	return vec, ok
}

// Mean averages the vectors of the words it recognises, and reports how many
// that was.
//
// Unknown words are skipped rather than treated as zero. A zero vector is not
// the absence of a word, it is a point in the middle of the space, and
// averaging one in would drag every phrase containing an unknown word towards
// the same place.
func (v *Vectors) Mean(words []string) ([]float32, int) {
	if v == nil || v.dim == 0 {
		return nil, 0
	}

	sum := make([]float32, v.dim)
	var known int
	for _, w := range words {
		vec, ok := v.Vector(w)
		if !ok {
			continue
		}
		for i, f := range vec {
			sum[i] += f
		}
		known++
	}
	if known == 0 {
		return nil, 0
	}

	for i := range sum {
		sum[i] /= float32(known)
	}
	return sum, known
}

// maxWords bounds how many words are loaded. Vector files run to millions of
// entries and hundreds of megabytes; the frequent words carry nearly all the
// value for judging a link, and they come first in every published file.
const maxWords = 200_000

// Load reads a vector file, transparently decompressing a gzipped one.
func Load(path string) (*Vectors, error) {
	f, err := os.Open(path) //nolint:gosec // the path is operator supplied
	if err != nil {
		return nil, fmt.Errorf("open vectors: %w", err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("read vectors %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}

	vecs, err := Read(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vecs, nil
}

// Read parses vectors from a reader.
func Read(r io.Reader) (*Vectors, error) {
	out := &Vectors{words: map[string][]float32{}}

	sc := bufio.NewScanner(r)
	// Vector lines are long: 300 numbers is several kilobytes, and the default
	// buffer would stop partway through a file with no obvious reason.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var line int
	for sc.Scan() {
		line++
		fields := strings.Fields(sc.Text())

		// word2vec files open with a "<count> <dim>" header, which is not a
		// word and would otherwise be loaded as one.
		if line == 1 && len(fields) == 2 {
			if _, err := strconv.Atoi(fields[0]); err == nil {
				if _, err := strconv.Atoi(fields[1]); err == nil {
					continue
				}
			}
		}
		if len(fields) < 3 {
			continue
		}

		word := strings.ToLower(fields[0])
		nums := fields[1:]

		if out.dim == 0 {
			out.dim = len(nums)
		}
		// A file whose rows disagree on width is corrupt, and silently keeping
		// the rows that happen to match would make similarity meaningless in a
		// way no error would ever surface.
		if len(nums) != out.dim {
			return nil, fmt.Errorf("line %d: %d values, expected %d", line, len(nums), out.dim)
		}

		vec := make([]float32, out.dim)
		for i, s := range nums {
			f, err := strconv.ParseFloat(s, 32)
			if err != nil {
				return nil, fmt.Errorf("line %d: %q is not a number", line, s)
			}
			vec[i] = float32(f)
		}

		if _, seen := out.words[word]; !seen {
			out.words[word] = vec
		}
		if len(out.words) >= maxWords {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read vectors: %w", err)
	}
	if len(out.words) == 0 {
		return nil, fmt.Errorf("no vectors found")
	}
	return out, nil
}
