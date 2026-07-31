package pattern

import (
	"regexp"
	"testing"

	"github.com/rangertaha/scour/internal/wom/internal/schema"
)

func TestDateShapeCoversRealFormats(t *testing.T) {
	re := regexp.MustCompile(ShapePrior(schema.TypeDate))

	ok := []string{
		"Fri, 31 Jul 2026 07:00:00 GMT",
		"Fri, 31 Jul 2026 07:00:00 +0000",
		"31 Jul 2026 07:00:00 GMT",
		"Tue, 14 Mar 2026 09:00 GMT",
		"2026-03-14T09:00:00Z",
		"2026-03-14T09:00:00+01:00",
		"2026-03-14T09:00:00.123Z",
		"2026-03-14 09:00:00",
		"2026-03-14",
		"14/03/2026",
		"March 14, 2026",
	}
	for _, v := range ok {
		m := re.FindStringSubmatch(v)
		if m == nil {
			t.Errorf("date shape rejects %q", v)
			continue
		}
		if m[1] == "" {
			t.Errorf("%q captured nothing", v)
		}
	}

	for _, v := range []string{"", "not a date", "Ford F-150", "42,000"} {
		if re.MatchString(v) {
			t.Errorf("date shape accepts %q", v)
		}
	}
}
