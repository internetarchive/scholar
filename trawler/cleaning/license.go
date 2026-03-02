package cleaning

import (
	"net/url"
	"strings"
)

// licenseSlugMap maps normalized license URLs to short slug identifiers.
// Only licenses that are at least partially OA are included.
// Based on popular licenses found in Crossref metadata.
var licenseSlugMap = map[string]string{
	"//creativecommons.org/publicdomain/mark/1.0":                     "CC-0",
	"//creativecommons.org/publicdomain/mark/1.0/deed.de":             "CC-0",
	"//creativecommons.org/publicdomain/zero/1.0":                     "CC-0",
	"//creativecommons.org/publicdomain/zero/1.0/legalcode":           "CC-0",
	"//creativecommons.org/share-your-work/public-domain/cc0":         "CC-0",
	"//creativecommons.org/licenses/by/2.0":                           "CC-BY",
	"//creativecommons.org/licenses/by/3.0":                           "CC-BY",
	"//creativecommons.org/licenses/by/4.0":                           "CC-BY",
	"//creativecommons.org/licenses/by-sa/3.0":                        "CC-BY-SA",
	"//creativecommons.org/licenses/by-sa/4.0":                        "CC-BY-SA",
	"//creativecommons.org/licenses/by-nd/3.0":                        "CC-BY-ND",
	"//creativecommons.org/licenses/by-nd/4.0":                        "CC-BY-ND",
	"//creativecommons.org/licenses/by-nc/3.0":                        "CC-BY-NC",
	"//creativecommons.org/licenses/by-nc/4.0":                        "CC-BY-NC",
	"//creativecommons.org/licenses/by-nc-sa/3.0":                     "CC-BY-NC-SA",
	"//creativecommons.org/licenses/by-nc-sa/4.0":                     "CC-BY-NC-SA",
	"//creativecommons.org/licenses/by-nc-nd/3.0":                     "CC-BY-NC-ND",
	"//creativecommons.org/licenses/by-nc-nd/4.0":                     "CC-BY-NC-ND",
	"//spdx.org/licenses/cc0-1.0.json":                                "CC-0",
	"//spdx.org/licenses/cc-by-1.0.json":                              "CC-BY",
	"//spdx.org/licenses/cc-by-4.0.json":                              "CC-BY",
	"//spdx.org/licenses/cc-by-nc-4.0.json":                           "CC-BY-NC",
	"//spdx.org/licenses/cc-by-sa-3.0.json":                           "CC-BY-SA",
	"//spdx.org/licenses/cc-by-sa-4.0.json":                           "CC-BY-SA",
	"//spdx.org/licenses/mit.json":                                    "MIT",
	"//spdx.org/licenses/ogl-canada-2.0.json":                         "OGL-Canada",
	"//www.elsevier.com/open-access/userlicense/1.0":                  "ELSEVIER-USER-1.0",
	"//www.elsevier.com/tdm/userlicense/1.0":                          "ELSEVIER-USER-1.0",
	"//www.karger.com/services/siteLicenses":                          "KARGER",
	"//archaeologydataservice.ac.uk/advice/termsofuseandaccess.xhtml": "ADS-UK",
	"//archaeologydataservice.ac.uk/advice/termsofuseandaccess":       "ADS-UK",
	"//homepage.data-planet.com/terms-use":                            "SAGE-DATA-PLANET",
	"//publikationen.bibliothek.kit.edu/kitopen-lizenz":               "KIT-OPEN",
	"//pubs.acs.org/page/policy/authorchoice_ccby_termsofuse.html":    "CC-BY",
	"//pubs.acs.org/page/policy/authorchoice_termsofuse.html":         "ACS-CHOICE",
	"//www.ametsoc.org/pubsreuselicenses":                             "AMETSOC",
	"//www.apa.org/pubs/journals/resources/open-access.aspx":          "APA",
	"//www.biologists.com/user-licence-1-1":                           "BIOLOGISTS-USER",
	"//www.gnu.org/licenses/gpl-3.0.en.html":                          "GPLv3",
	"//www.gnu.org/licenses/old-licenses/gpl-2.0.en.html":             "GPLv2",
	"//arxiv.org/licenses/nonexclusive-distrib/1.0":                   "ARXIV-1.0",
}

// LicenseSlugLookup normalizes a license URL and returns a short slug, or
// empty string if unrecognized.
func LicenseSlugLookup(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	u, err := url.Parse(strings.ToLower(rawURL))
	if err != nil {
		return ""
	}

	path := strings.TrimSuffix(u.Path, "/")
	if u.Host == "creativecommons.org" {
		path = strings.TrimSuffix(path, "/legalcode")
		path = strings.TrimSuffix(path, "/uk")
	}

	return licenseSlugMap["//"+u.Host+path]
}
