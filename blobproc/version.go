package blobproc

// Version of library and cli tools. Set at build time from the git tag via
// -ldflags "-X .../blobproc.Version=<version>" (see Makefile); "dev" otherwise.
var Version = "dev"
