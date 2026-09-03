// SPDX-License-Identifier: GPL-3.0-or-later

package run

import "time"

// Hold is [Run.hold] with the fetch timeout the run would use, for the test
// that pins the lease against what one unit of work can actually take.
func (r *Run) Hold() time.Duration {
	fetch, err := r.job.Downloader.RequestTimeout()
	if err != nil {
		return 0
	}
	return r.hold(fetch)
}
