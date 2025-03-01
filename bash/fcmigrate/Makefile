.PHONY: dump
dump: data/${CONTAINERS_OUT} data/${CREATORS_OUT} data/${WORKS_OUT}
data/${RELEASES_OUT} data/${RELEASES_EXTID_OUT} data/${RELEASES_ABSTRACT_OUT} data/${RELEASES_CONTRIB_OUT} data/${RELEASES_REF_OUT} data/${FILES_OUT} data/${FILES_URL_OUT} data/${FILESETS_OUT} data/${FILESETS_URL_OUT} data/${FILESETS_FILE_OUT} data/${WEBCAPTURES_OUT} data/${WEBCAPTURES_URL_OUT} data/${WEBCAPTURES_CDX_OUT}

data:
	mkdir data

data/${CONTAINERS_OUT}: data
	source dump.sh; dump_containers

data/${CREATORS_OUT}: data
	source dump.sh; dump_creators

data/${WORKS_OUT}: data
	source dump.sh; dump_works

data/${RELEASES_OUT}: data 
	source dump.sh; dump_releases

data/${RELEASES_EXTID_OUT}: data
	source dump.sh; dump_releaseextid

data/${RELEASES_ABSTRACT_OUT}: data
	source dump.sh; dump_releaseabstract

data/${RELEASES_CONTRIB_OUT}: data
	source dump.sh; dump_releasecontrib

data/${RELEASES_REF_OUT}: data
	source dump.sh; dump_releaseref

data/${FILES_OUT}: data
	source dump.sh; dump_files

data/${FILES_URL_OUT}: data
	source dump.sh; dump_fileurl

data/${FILESETS_OUT}: data
	source dump.sh; dump_fileset

data/${FILESETS_URL_OUT}: data
	source dump.sh; dump_fileseturls

data/${FILESETS_FILE_OUT}: data
	source dump.sh; dump_filesetfiles

data/${WEBCAPTURES_OUT}: data
	source dump.sh; dump_webcaptures

data/${WEBCAPTURES_URL_OUT}: data
	source dump.sh; dump_webcapture_urls

data/${WEBCAPTURES_CDX_OUT}: data
	source dump.sh; dump_webcapture_cdx
