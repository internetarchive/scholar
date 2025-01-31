from django.db import models
from django.db.models.functions import Now


class Entity(models.Model):
    created = models.TimeField(db_default=Now())
    updated = models.TimeField(db_default=Now(), db_index=True)
    deleted = models.TimeField(null=True)
    source = models.CharField()
    hidden = models.BooleanField(default=False)
    hidden_reason = models.TextField()
    extra = models.JSONField()

    class Meta:
        abstract = True


class Container(Entity):
    name = models.CharField()
    container_type = models.CharField()
    publisher = models.CharField()
    issnl = models.CharField(db_index=True)
    issne = models.CharField(db_index=True)
    issnp = models.CharField(db_index=True)
    wikidata_qid = models.CharField(db_index=True)
    publication_status = models.CharField()


class Work(Entity):
    pass


class Creator(Entity):
    display_name = models.CharField()
    given_name = models.CharField()
    surname = models.CharField()
    orcid = models.CharField(db_index=True)
    wikidata_qid = models.CharField(db_index=True)


class Release(Entity):
    work = models.ForeignKey(Work, on_delete=models.CASCADE, db_index=True)
    container = models.ForeignKey(Container, on_delete=models.CASCADE, db_index=True)  # noqa: E501

    title = models.CharField()
    original_title = models.CharField()
    subtitle = models.CharField()

    release_type = models.CharField()
    release_stage = models.CharField()
    release_date = models.DateField(null=True)
    release_year = models.SmallIntegerField(null=True)

    volume = models.CharField()
    issue = models.CharField()
    pages = models.CharField()

    publisher = models.CharField()
    language = models.CharField()
    license_slug = models.CharField()

    number = models.CharField()
    version = models.CharField()

    withdrawn_status = models.CharField()
    withdrawn_date = models.DateField()
    withdrawn_year = models.SmallIntegerField()

    refs = models.JSONField()


class ReleaseExtId(models.Model):
    """a mapping of external ID types to values.

    Possible types:
    - doi
    - pmid
    - pmcid
    - wikidata_qid
    - core_id
    - ark
    - arxiv
    - dblp
    - doaj
    - hdl
    - isbn13
    - jstor
    - mag
    """
    release = models.ForeignKey(Release, on_delete=models.CASCADE)
    id_type = models.CharField()
    id_value = models.CharField()

    class Meta:
        indexes = [
                models.Index(fields=['release_id', 'id_type']),
                models.Index(fields=['id_type', 'id_value']),
                ]


class ReleaseAbstract(models.Model):
    """The text of a release's abstract"""
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    mimetype = models.CharField()
    lang = models.CharField()
    sha1 = models.CharField(max_length=40)
    content = models.TextField()


class ReleaseContrib(models.Model):
    """A record of a given author's contribution to a release."""
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    creator = models.ForeignKey(Creator, on_delete=models.CASCADE,
                                db_index=True)
    raw_name = models.CharField()
    given_name = models.CharField()
    surname = models.CharField()
    role = models.CharField()
    raw_affiliation = models.CharField()
    index_val = models.SmallIntegerField()
    extra = models.JSONField()


class ReleaseRef(models.Model):
    """A reference (citation) from one paper to another"""
    position = models.SmallIntegerField()
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    target_release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                       related_name="target", db_index=True)


class BaseFile(models.Model):
    size_bytes = models.BigIntegerField()
    sha1 = models.CharField(max_length=40)
    sha256 = models.CharField(max_length=64)
    md5 = models.CharField(max_length=32)
    mimetype = models.CharField()

    class Meta:
        abstract = True


class ReleaseFile(Entity, BaseFile):
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    content_scope = models.CharField()


class FileURL(models.Model):
    file = models.ForeignKey(ReleaseFile, on_delete=models.CASCADE,
                             db_index=True)
    rel = models.CharField()
    url = models.CharField()


class Fileset(Entity):
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    content_scope = models.CharField()


class FilesetFile(Entity, BaseFile):
    fileset = models.ForeignKey(Fileset, on_delete=models.CASCADE,
                                db_index=True)


class Webcapture(Entity):
    release = models.ForeignKey(Release, on_delete=models.CASCADE,
                                db_index=True)
    original_url = models.TextField()
    ts = models.TimeField()
    content_scope = models.CharField()


class WebcaptureCDX(models.Model):
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE,
                                   db_index=True)
    surt = models.TextField()
    ts = models.TimeField(null=False)
    url = models.TextField()
    mimetype = models.CharField()
    status_code = models.SmallIntegerField()
    sha1 = models.CharField(max_length=40)
    sha256 = models.CharField(max_length=64)
    size_bytes = models.BigIntegerField()


class WebcaptureURL(models.Model):
    webcapture = models.ForeignKey(Webcapture, on_delete=models.CASCADE,
                                   db_index=True)
    rel = models.CharField()
    url = models.CharField()
