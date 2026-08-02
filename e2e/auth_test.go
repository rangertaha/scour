// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

// getAuth fetches a path with basic credentials.
func getAuth(t *testing.T, base, p, user, pass string) (answer, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+p, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", p, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

// pressPagePaths is every URL behind the credential.
var pressPagePaths = []string{"/private/", "/private/board-minutes.html", "/private/tender.html"}

// Nothing in the press area answers without a credential, and the refusal is a
// proper 401 with a challenge rather than a 403 or a redirect.
func TestThePressAreaRefusesAnonymousRequests(t *testing.T) {
	srv := Server(t)

	for _, p := range pressPagePaths {
		resp, body := get(t, srv.URL, p)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", p, resp.StatusCode)
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		if !strings.HasPrefix(challenge, "Basic ") || !strings.Contains(challenge, PressRealm) {
			t.Errorf("%s challenged with %q", p, challenge)
		}
		// The refusal says where the credentials are, which is what makes this
		// a puzzle with an answer rather than a wall.
		if !strings.Contains(body, "/files/press-credentials.pdf") {
			t.Errorf("%s does not point at the media pack:\n%s", p, body)
		}
	}

	// Wrong credentials are refused the same way as none at all.
	if resp, _ := getAuth(t, srv.URL, "/private/", PressUser, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d", resp.StatusCode)
	}
	if resp, _ := getAuth(t, srv.URL, "/private/", "nobody", PressPass); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong username = %d", resp.StatusCode)
	}
}

// The whole point of the section: the way in is in a PDF, so read the PDF and
// walk in. If this passes, a crawler that can extract PDF text can do the same.
func TestTheCredentialsInTheMediaPackOpenThePressArea(t *testing.T) {
	srv := Server(t)

	// Fetch the media pack the way anything else would.
	resp, body := get(t, srv.URL, "/files/press-credentials.pdf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the media pack = %d", resp.StatusCode)
	}

	// Extract its text with the reader scour itself uses.
	raw := []byte(body)
	doc, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the media pack does not parse: %v", err)
	}
	stream, err := doc.GetPlainText()
	if err != nil {
		t.Fatalf("no text in the media pack: %v", err)
	}
	extracted, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	text := string(extracted)

	// The credentials are in there, in a form somebody has to read.
	if !credentialsInPDF(text) {
		t.Fatalf("the media pack no longer carries the credentials the door expects:\n%s", text)
	}

	// Now use what the PDF said, parsed out of the text rather than taken from
	// the constants, so this proves the document is sufficient on its own.
	user, pass := credentialsFrom(text)
	if user == "" || pass == "" {
		t.Fatalf("could not read a username and password out of:\n%s", text)
	}
	for _, p := range pressPagePaths {
		resp, page := getAuth(t, srv.URL, p, user, pass)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s with the media pack's credentials = %d", p, resp.StatusCode)
			continue
		}
		if strings.Contains(page, "Press area</title>") && p != "/private/" {
			t.Errorf("%s returned the refusal page with a 200", p)
		}
	}

	// And the protected pages carry something the public side does not.
	if _, minutes := getAuth(t, srv.URL, "/private/board-minutes.html", user, pass); !strings.Contains(minutes, "standby clause") {
		t.Errorf("the board minutes are missing their content:\n%s", minutes)
	}
}

// credentialsFrom reads a username and password out of the media pack's text,
// the way something following the trail would have to.
func credentialsFrom(text string) (user, pass string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "username:"); ok {
			user = strings.TrimSpace(rest)
		}
		if rest, ok := strings.CutPrefix(line, "password:"); ok {
			pass = strings.TrimSpace(rest)
		}
	}
	return user, pass
}

// The media pack is only useful if something links to it, and the trail has to
// start on an ordinary page rather than at a path somebody has to know.
func TestTheMediaPackIsLinkedFromHTML(t *testing.T) {
	srv := Server(t)

	var linked []string
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, err := fs.ReadFile(FS(), p)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "press-credentials.pdf") {
			linked = append(linked, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) == 0 {
		t.Fatal("no HTML page links the media pack, so nothing can find its way in")
	}
	t.Logf("the media pack is linked from %v", linked)

	// And the link resolves, in whatever form the page wrote it.
	if resp, _ := get(t, srv.URL, "/files/press-credentials.pdf"); resp.StatusCode != http.StatusOK {
		t.Errorf("the linked media pack = %d", resp.StatusCode)
	}
}

// A PDF must never be rewritten, and the media pack is the one where it would
// matter most: a shifted byte breaks the xref and the credentials become
// unreadable.
func TestTheMediaPackSurvivesBeingServed(t *testing.T) {
	srv := Server(t)

	onDisk, err := fs.ReadFile(FS(), "files/press-credentials.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, served := get(t, srv.URL, "/files/press-credentials.pdf")
	if string(onDisk) != served {
		t.Errorf("the served PDF differs from the embedded one: %d vs %d bytes",
			len(onDisk), len(served))
	}
}
