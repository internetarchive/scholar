from django.db import models
from django.db.models.functions import Now


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
            help_text="explanatory information for the value of hidden")
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
    container_type = models.CharField()
    publisher = models.CharField()
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
            choices=[
                "abstract",
                "article",
                "article-journal",
                "article-newspaper",
                "book",
                "chapter",
                "component",
                "dataset",
                "editorial",
                "entry",
                "graphic",
                "interview",
                "legal_case",
                "legislation",
                "letter",
                "paper-conference",
                "peer_review",
                "post",
                "post-weblog",
                "report",
                "retraction",
                "review-book",
                "software",
                "song",
                "speech",
                "standard",
                "stub",
                "thesis",
                ],
            null=True, blank=True)

    release_stage = models.CharField(
            help_text="Location of release in publishing pipeline",
            choices=[
                 "accepted",
                 "draft",
                 "published",
                 "retraction",
                 "submitted",
                 "updated",
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
        "doi",
        "pmid",
        "pmcid",
        "wikidata_qid",
        "core_id",
        "ark",
        "arxiv",
        "dblp",
        "doaj",
        "hdl",
        "isbn13",
        "jstor",
        "mag",
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
    sha1 = models.CharField(max_length=40)
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
                "author",
                "editor",
                "translator"],
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


# TODO continue audit/documentation here
class BaseFile(models.Model):
    size_bytes = models.BigIntegerField()
    sha1 = models.CharField(max_length=40)
    sha256 = models.CharField(max_length=64)
    md5 = models.CharField(max_length=32)
    mimetype = models.CharField()

    class Meta:
        abstract = True


class ReleaseFile(Entity, BaseFile):
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    content_scope = models.CharField()


class FileURL(models.Model):
    file = models.ForeignKey(ReleaseFile, on_delete=models.CASCADE)
    rel = models.CharField()
    url = models.CharField()


class Fileset(Entity):
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    content_scope = models.CharField()


class FilesetFile(Entity, BaseFile):
    fileset = models.ForeignKey(Fileset, on_delete=models.CASCADE)


class Webcapture(Entity):
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    original_url = models.TextField()
    ts = models.DateTimeField()
    content_scope = models.CharField()


class WebcaptureCDX(models.Model):
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE)
    surt = models.TextField()
    ts = models.DateTimeField()
    url = models.TextField()
    mimetype = models.CharField()
    status_code = models.SmallIntegerField()
    sha1 = models.CharField(max_length=40)
    sha256 = models.CharField(max_length=64)
    size_bytes = models.BigIntegerField()


class WebcaptureURL(models.Model):
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE)
    rel = models.CharField()
    url = models.CharField()
