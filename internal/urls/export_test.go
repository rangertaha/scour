// SPDX-License-Identifier: GPL-3.0-or-later

package urls

// Clean is [clean], for the test that pins it against RFC 3986 section 5.2.4.
//
// Exported to the test rather than tested through Normalise because the
// algorithm is written out by hand here, and a path that survives Normalise
// tells you the whole pipeline agreed rather than that this step is right.
var Clean = clean
