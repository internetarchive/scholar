package crossref

import "strings"

// The following was taken directly from old fatcat code. It might behoove us
// to review popular licenses on crossref.

// These are based, informally, on sorting the most popular licenses found in
// Crossref metadata. There were over 500 unique strings and only a few most
// popular are here; many were variants of the CC URLs. Would be useful to
// normalize CC licenses better.
// The current norm is to only add license slugs that are at least partially OA.
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

	// skip these non-OA licenses
	//# //iopscience.iop.org/page/copyright is closed
	//# //www.acm.org/publications/policies/copyright_policy#Background is closed
	//# //www.ieee.org/publications_standards/publications/rights/ieeecopyrightform.pdf is 404 (!)
	//# skip these TDM licenses; they don't apply to content
	//# "//www.springer.com/tdm": "SPRINGER-TDM",
	//# "//journals.sagepub.com/page/policies/text-and-data-mining-license": "SAGE-TDM",
	//# "//doi.wiley.com/10.1002/tdm_license_1.1": "WILEY-TDM-1.1",
	//# //onlinelibrary.wiley.com/termsAndConditions doesn't seem like a license
	//# //www.springer.com/tdm doesn't seem like a license
	//# //rsc.li/journals-terms-of-use is closed for vor (am open)
}

func licenseSlugLookup(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	rawURL = strings.ToLower(rawURL)
	rawURL = strings.TrimSuffix(rawURL, "/")
	rawURL = strings.ReplaceAll(rawURL, "https://", "//")
	rawURL = strings.ReplaceAll(rawURL, "http://", "//")
	if strings.Contains(rawURL, "creativecommons.org") {
		rawURL = strings.ReplaceAll(rawURL, "/legalcode", "")
		rawURL = strings.ReplaceAll(rawURL, "/uk", "")
	}
	return licenseSlugMap[rawURL]
}
