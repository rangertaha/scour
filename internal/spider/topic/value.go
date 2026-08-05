// SPDX-License-Identifier: GPL-3.0-or-later

package topic

import (
	"strconv"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/extract"
)

// value is the score as an extracted value.
//
// It carries which classifier produced it, because a score of 0.71 is
// meaningless without knowing what was being scored and by which training of
// it. A record that says climate@7 can be re-read when climate@8 disagrees.
func value(score float64, ref classify.Ref) *extract.Value {
	return &extract.Value{
		Text: strconv.FormatFloat(score, 'f', 3, 64),
		Raw:  strconv.FormatFloat(score, 'f', 3, 64),
		From: ref.String(),
		How:  "classified",
	}
}
