// SPDX-License-Identifier: GPL-3.0-or-later

package cache

// Driver names, declared in both builds so a configuration means the same thing
// whether or not the object stores were compiled in.
const (
	driverLocal = "local"
	driverS3    = "s3"
	driverGCS   = "gcs"
)
