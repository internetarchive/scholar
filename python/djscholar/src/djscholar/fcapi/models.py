from django.db import models
from django.db.models.functions import Now

URL_MAX_LENGTH = 100000 # we have some weird URLs.
SHA1_MAX_LENGTH = 40
SHA256_MAX_LENGTH = 64
MD5_MAX_LENGTH = 32


class Entity(models.Model):
    """
    This abstract model defines the common columns we want for all of the items
    for which we store metadata records (ie, papers, journals, authors).

    We index the updated column on all entities since a common access pattern
    for fatcat is to determine what has been newly added or changed in the
    catalog (for example, to see what to index or re-index in scholar's
    elasticsearch).
    """
    created = models.DateTimeField(db_default=Now())
    updated = models.DateTimeField(db_default=Now(), auto_now=True,
                                   db_index=True)
    source = models.CharField(
            help_text="an arbitrary string denoting the data source whence a record was found",
            db_index=True)
    hidden_reason = models.TextField(
            help_text="explanatory information why an entity was hidden",
            null=True, blank=True)
    hidden_when = models.DateTimeField(
            help_text="when a given record was hidden",
            null=True)
    extra = models.JSONField(
            help_text="arbitrary storage for additional key/value data found in upstream sources")

    class Meta:
        abstract = True


class Container(Entity):
    """
    This entity represents, most commonly, an academic journal that publishes
    papers. However, it might also refer to a conference that published
    proceedings.
    """
    name = models.CharField()
    container_type = models.CharField(
            choices=[
                ("blog", "blog"),
                ("book-series", "book-series"),
                ("conference", "conference"),
                ("conference-series", "conference-series"),
                ("journal", "journal"),
                ("magazine", "magazine"),
                ("proceedings", "proceedings"),
                ("repository", "repository"),
                ],
            null=True, blank=True)
    publisher = models.CharField(
            help_text="name of container's publisher")
    issnl = models.CharField(
            help_text="an ISSN-L, or linking ISSN. This is a grouping ISSN for publications that print in various media (eg, print and digital",
            unique=True, null=True, blank=True)
    issne = models.CharField(
            help_text="an e-ISSN, or electronic ISSN. for digital versions of publications. This can be linked to a p-ISSN (issnp column) via an ISSN-L.",
            unique=True, null=True, blank=True)
    issnp = models.CharField(
            help_text="a p-ISSN, or print ISSN. for print versions of publications. This can be linked to an e-ISSN (issne column) via an ISSN-L.",
            unique=True, null=True, blank=True)
    wikidata_qid = models.CharField(
            help_text="ID from the wikidata project. See https://www.wikidata.org/wiki/Wikidata:Identifiers",
            unique=True, null=True, blank=True)


class Work(Entity):
    """
    This entity is for the logical grouping of releases. Imagine multiple
    versions of a paper: a pre-print or two, a published version, a retracted
    version. All are grouped under a single "work." Thus, all releases have an
    associated work record. However, many works may only contain a single
    release.

    A work entity has no columns of its own; a work is really just an ID.
    """
    pass


class Creator(Entity):
    """
    This entity represents someone who contributed to a work: perhaps the
    author of a paper or the creator of a conference talk.

    We require that creators at least have a display_name. with luck we'll have
    given_name and surname columns populated, too.
    """
    display_name = models.CharField(
            help_text="full name of a human to show in web front-ends")
    given_name = models.CharField(
            help_text="'first' name of a human depending on context",
            null=True, blank=True)
    surname = models.CharField(
            help_text="'last' name of a human depending on context",
            null=True, blank=True)
    orcid = models.CharField(
            help_text="external, unique identifier of a human author. See https://orcid.org/",
            unique=True, null=True, blank=True)


class Release(Entity):
    """
    The bulk of our data. A release is most likely an academic paper, but we
    track things like a conference talk or book a release also.
    """
    work = models.ForeignKey(
            Work,
            help_text="the work under which this release is grouped",
            on_delete=models.CASCADE)
    container = models.ForeignKey(
            Container,
            help_text="the thing in which this release was published. for example, for a paper, its container is likely an academic journal",
            on_delete=models.CASCADE)

    title = models.CharField(help_text="a title for the release")
    original_title = models.CharField(
            help_text="title in original language if title field value has been tranlasted",
            null=True, blank=True)
    subtitle = models.CharField(
            help_text="subtitle, if any, for a release",
            null=True, blank=True)

    release_type = models.CharField(
            help_text="Kind of release. Mostly, we have article or article-journal. The choices were extracted from the original fatcat dataset",
            # this is an arbitrary subset of types from the Citation Style
            # Language. I included all the ones in the previous fatcat schema
            # and added some that might come up in the future. The types I left
            # out are things we are unlikely to ever track as "releases".
            choices=[
                # in CSL:
                ("article", "article"),
                ("article-journal", "article-journal"),
                ("article-magazine", "article-magazine"),
                ("article-newspaper", "article-newspaper"),
                ("book", "book"),
                ("broadcast", "broadcast"),
                ("chapter", "chapter"),
                ("dataset", "dataset"),
                ("entry", "entry"),
                ("event", "event"),
                ("figure", "figure"),
                ("graphic", "graphic"),
                ("hearing", "hearing"),
                ("interview", "interview"),
                ("legal_case", "legal_case"),
                ("legislation", "legislation"),
                ("manuscript", "manuscript"),
                ("map", "map"),
                ("motion_picture", "motion_picture"),
                ("musical_score", "musical_score"),
                ("pamphlet", "pamphlet"),
                ("paper-conference", "paper-conference"),
                ("patent", "patent"),
                ("personal_communication", "personal_communication"),
                ("post", "post"),
                ("post-weblog", "post-weblog"),
                ("regulation", "regulation"),
                ("report", "report"),
                ("review", "review"),
                ("review-book", "review-book"),
                ("software", "software"),
                ("song", "song"),
                ("speech", "speech"),
                ("standard", "standard"),
                ("thesis", "thesis"),
                ("treaty", "treaty"),
                ("webpage", "webpage"),

                # not in CSL, fatcat extensions:

                # releases that are only an abstract of a larger work. In
                # particular, translations. Many are granted DOIs.
                ("abstract", "abstract"),

                # columns, "in this issue", and other content published along
                # peer-reviewed content in journals. Many are granted DOIs.
                ("editorial", "editorial"),

                # sub-components of a full paper or other work. Eg, tables, or individual files as part of a dataset.
                ("component", "component"),

                ("peer_review", "peer_review"),

                # releases which have notable external identifiers, and thus
                # are included "for completeness", but don't seem to represent
                # a "full work".
                ("stub", "stub"),

                # used when a release is retracted; release_stage should match this
                ("retraction", "retraction"),
                ],
            null=True, blank=True)

    release_stage = models.CharField(
            help_text="Location of release in publishing pipeline",
            choices=[
                 ("accepted", "accepted"),
                 ("draft", "draft"),
                 ("published", "published"),
                 ("retraction", "retraction"),
                 ("submitted", "submitted"),
                 ("updated", "updated"),
                ],
            null=True, blank=True)
    release_date = models.DateField(
            help_text="exact date on which this release was published, if known",
            null=True, blank=True)
    release_year = models.SmallIntegerField(
            help_text="Year in which this release was published. Separate from release_date since we often only know a year",
            null=True)

    volume = models.CharField(
            help_text="Volume of parent container in which this was published",
            null=True, blank=True)
    issue = models.CharField(
            help_text="Issue of parent container in which this was published",
            null=True, blank=True)
    pages = models.CharField(
            help_text="Free form text describing the page or page range that contains this release in its parent container",
            null=True, blank=True)
    number = models.CharField(
            help_text="Arbitrary number in parent container in which this release was published. For example, technical reports use numbers instead of volumes.",
            null=True, blank=True)
    version = models.CharField(
            help_text="Arbitrary version string. Might be used by technical reports or software packages",
            null=True, blank=True)

    publisher = models.CharField(
            help_text="Name of publisher, if known",
            null=True, blank=True)
    language = models.CharField(
            help_text="Primary language of release content. Two-letter RFC1766/ISO639-1 language code.",
            max_length=2,
            null=True, blank=True)
    license_slug = models.CharField(
            help_text="short name for a license covering this release. for example, 'CC-BY-NA'",
            null=True, blank=True)

    withdrawn_status = models.CharField(
            help_text="free form field for stating why a release has been withdrawn. currently used values: concern, retracted, safety, spam, withdrawn.",
            blank=True, null=True)

    refs = models.JSONField(
            help_text="a JSON blob describing the citations of this release.",
            null=True, blank=True)


class ReleaseExtId(models.Model):
    """
    This model maps releases to a set of external identifiers expressed as key
    value pairs. Most releases in our system will have at least a doi.
    """
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    id_type = models.CharField(choices=[
        ("doi", "doi"),
        ("pmid", "pmid"),
        ("pmcid", "pmcid"),
        ("wikidata_qid", "wikidata_qid"),
        ("core_id", "core_id"),
        ("ark", "ark"),
        ("arxiv", "arxiv"),
        ("dblp", "dblp"),
        ("doaj", "doaj"),
        ("hdl", "hdl"),
        ("isbn13", "isbn13"),
        ("jstor", "jstor"),
        ("mag", "mag"),
        ])
    id_value = models.CharField()

    class Meta:
        indexes = [
                # we need to quickly query by external value
                models.Index(fields=['id_type', 'id_value']),
                ]


class ReleaseAbstract(models.Model):
    """The text of a release's abstract"""
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    mimetype = models.CharField(default="text/plain")
    language = models.CharField(
            help_text="Primary language of abstract. Two-letter RFC1766/ISO639-1 language code.",
            max_length=2,
            null=True, blank=True)
    license_slug = models.CharField(
            help_text="short name for a license covering this release. for example, 'CC-BY-NA'.",
            null=True, blank=True)
    sha1 = models.CharField(max_length=SHA1_MAX_LENGTH, db_index=True)
    content = models.TextField()


class ReleaseContrib(models.Model):
    """A record of a given author's contribution to a release."""
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    creator = models.ForeignKey(Creator, on_delete=models.CASCADE)
    raw_name = models.CharField(
            help_text="Name of the author as listed in the reference. If this reference is matched to an author in our database, this value might differ from the linked author's display name.")
    given_name = models.CharField(
            help_text="'first' name of a human depending on context",
            null=True, blank=True)
    surname = models.CharField(
            help_text="'last' name of a human depending on context",
            null=True, blank=True)
    role = models.CharField(
            help_text="role played by contributor",
            choices=[
                ("author", "author"),
                ("editor", "editor"),
                ("translator", "translator")],
            null=True, blank=True)
    raw_affiliation = models.CharField(
            help_text="Name of instituion or organization to which contributor belonged",
            null=True, blank=True)
    index_val = models.SmallIntegerField(
            help_text="Position in list of contributors")
    extra = models.JSONField(
            help_text="JSON blob for additional metadata")


class ReleaseRef(models.Model):
    """A reference (citation) from one paper to another"""
    release = models.ForeignKey(
            Release,
            help_text="release in which this citation occurred",
            on_delete=models.CASCADE)
    position = models.SmallIntegerField(
            help_text="Position in list of references")
    target_release = models.ForeignKey(
            Release,
            on_delete=models.CASCADE,
            help_text="Release referenced by this citation",
            related_name="target")


class BaseFile(models.Model):
    """
    We track a few different kinds of files so this abstract base class
    collects their common columns.
    """
    size_bytes = models.BigIntegerField(
            help_text="size in bytes of this file")
    sha1 = models.CharField(max_length=SHA1_MAX_LENGTH, db_index=True)
    sha256 = models.CharField(max_length=SHA256_MAX_LENGTH, db_index=True)
    md5 = models.CharField(max_length=MD5_MAX_LENGTH, db_index=True)
    mimetype = models.CharField()

    class Meta:
        abstract = True


class ReleaseFile(Entity, BaseFile):
    """
    A file associated with a release. Actual file content is stored in seaweedfs.
    """
    release = models.ForeignKey(Release, on_delete=models.CASCADE)


class FileURL(models.Model):
    """
    A URL at which a release's file can be found.
    """
    file = models.ForeignKey(ReleaseFile, on_delete=models.CASCADE)
    rel = models.CharField(choices=[
        ("web", "general public web site"),
        ("webarchive", "a resource in a long-term web archive"),
        ("repository", "a resource stored in an academic repository"),
        ("academicsocial", "academic social network content"),
        ("publisher", "a resource on a publisher's homepage"),
        ("aggregator", "full text aggregator or search engine like Semantic Scholar"),
        ("dweb", "content on a distributed or decentralized web protocol like dat:// or ipfs://"),
        ], default="web")
    url = models.URLField(max_length=URL_MAX_LENGTH)


class Fileset(Entity):
    """
    A set of files that should be associated with a release, possibly figures or datasets.
    """
    release = models.ForeignKey(Release, on_delete=models.CASCADE)


class FilesetFile(Entity, BaseFile):
    """
    A file within a fileset.
    """
    fileset = models.ForeignKey(Fileset, on_delete=models.CASCADE)


class Webcapture(Entity):
    """
    A complete record of release captured as a webpage snapshot.
    """
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    original_url = models.URLField(
            max_length=URL_MAX_LENGTH,
            help_text="base URL of the resource.")
    captured = models.DateTimeField(
            help_text="date and time of capture")


class WebcaptureCDX(models.Model):
    """
    A CDX line that constitutes part of a webcapture.
    """
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE)
    surt = models.TextField(help_text="sortable URL format")
    captured = models.DateTimeField(help_text="capture time")
    url = models.URLField(max_length=URL_MAX_LENGTH)
    mimetype = models.CharField()
    status_code = models.SmallIntegerField(help_text="HTTP status code")
    sha1 = models.CharField(max_length=SHA1_MAX_LENGTH)
    sha256 = models.CharField(max_length=SHA256_MAX_LENGTH)
    size_bytes = models.BigIntegerField()


class WebcaptureURL(models.Model):
    """
    A set of URLs at which a given web capture can be found.

    Can be wayback/memento instances, or direct links to a WARC file containing
    all the capture resources.  Often will only be a single archive. Order is not
    meaningful, and may not be preserved.
    """
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE)
    rel = models.CharField(choices=[
        ("warc", "warc"),
        ("wayback", "wayback"),
        ("webarchive", "webarchive"),
        ])
    url = models.URLField(max_length=URL_MAX_LENGTH)
