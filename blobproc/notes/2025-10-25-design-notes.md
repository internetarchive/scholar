# design notes

* blobproc turns PDF to a number of artifacts; similar to a derive
* it can also be thought of as a cache, everything can vaporize, and we can still recreate (with latency) the cache from archival holdings
* it is ok for now to hardcode all the derivations; if we need to change something we adjust, recompile, redeploy
* artifacts should be considered read only, but they can be overwritten
