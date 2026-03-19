import datetime
from http import HTTPStatus
from uuid import UUID
from typing import Annotated, Literal
from zoneinfo import ZoneInfo

import pydantic
from pydantic import AfterValidator
from django.db import transaction
from django.http import HttpResponse, HttpResponseRedirect, Http404
from ninja.pagination import paginate
from django.shortcuts import get_object_or_404
from ninja import NinjaAPI, Query, Schema, ModelSchema
from ninja.orm import create_schema
from ninja_apikey.security import APIKeyAuth

import djscholar.fcapi.models as m
from djscholar.fcapi.fcid import fcid2uuid

COMMON_ENTITY_FIELDS = ["id", "created", "updated", "extra", "source",
                        "hidden_reason", "hidden_when"]

COMMON_ENTITY_OPTIONAL_FIELDS = ["created", "updated", "extra", "hidden_reason",
                                 "hidden_when"]

v2api = NinjaAPI()
api_auth = APIKeyAuth()  # NB: uses X-API-Key header. use admin to create keys.

# crawl MVP
# TODO touch update column whenever an update route called
# TODO sort entities by updated or created time
# TODO query for releases that do not have associated files -- wantlist
# TODO use response= in all the decorators so the docs work

# post crawl MVP
# TODO filter hidden things
# TODO consider generalizing route implementations if it doesn't make
# signatures too hideous / doesn't break doc generation
# TODO should support the creation of creators via release creation/update?
# TODO use tags to split auth/unauth sections out in docs

# NB I hate that I have to use response= in addition to a return type. the
# latter should imply the former.

# In/Out schemas


class ContainerLookup(Schema):
    id_type: Literal["issnl", "issne", "issnp", "wikidata_qid", "legacy_ident"]
    id_value: str


class CreatorLookup(Schema):
    id_type: Literal["orcid", "legacy_ident"]
    id_value: str


def lower(s: str) -> str:
    return s.lower()


class FileLookup(Schema):
    id_type: Literal["sha1", "sha256", "md5", "legacy_ident"]
    id_value: Annotated[str, AfterValidator(lower)]


class ReleaseLookup(Schema):
    id_type: Literal[*[t[0]
                       for t
                       in m.RELEASE_EXT_ID_TYPES] + ["legacy_ident"]]
    id_value: str


class LegacyLookup(Schema):
    id_type: Literal["legacy_ident"]
    id_value: str

# Notes on schemas

# Once used in an argument, schemas appear in a nice "schemas" list in the API
# docs. we want that. I could not figure out a way to get things into that list
# otherwise. Frustratingly, the only way I can figure out how to control the
# name of the schema in that list is via the "name" parameter in create_schema
# -- otherwise it uses class name. However, create_schema is very clunky and
# harder to scan than the class based schema definitions.
# I do not like how implicit this is; it's especially confusing to not have
# using a schema in a return type register it in the API. I'd like to submit
# upstream the option to just explicitly name things when using the class based
# definition via the meta class.


class ReleaseExtIdSchema(ModelSchema):
    # NB not really optional; this is for creation of releases where a list of
    # this model is embedded.
    release_id: UUID | None

    class Meta:
        model = m.ReleaseExtId
        fields = ["id_type", "id_value"]


class ReleaseContribSchema(ModelSchema):
    # NB not really optional; this is for creation of releases where a list of
    # this model is embedded.
    release_id: UUID | None
    creator_id: UUID | None

    class Meta:
        model = m.ReleaseContrib
        fields = ["raw_name", "given_name", "surname", "role",
                  "raw_affiliation", "position", "extra"]


class ReleaseAbstractSchema(ModelSchema):
    # NB not really optional; this is for creation of releases where a list of
    # this model is embedded.
    release_id: UUID | None

    class Meta:
        model = m.ReleaseAbstract
        fields = ["sha1", "content", "language", "mimetype"]


class ReleaseRefSchema(ModelSchema):
    # NB not really optional; this is for creation of releases where a list of
    # this model is embedded.
    release_id: UUID | None
    target_release_id: UUID

    class Meta:
        model = m.ReleaseRef
        fields = ["position"]


class ReleaseSchema(ModelSchema):
    # will be created automatically if none
    work_id: UUID | None
    container_id: UUID | None
    extids: list[ReleaseExtIdSchema] = []
    contribs: list[ReleaseContribSchema] = []
    abstracts: list[ReleaseAbstractSchema] = []

    # NB the discrepancy between ReleaseRef and the citations field is due to
    # the 'refs' column on the release table. In old fatcat refs were derived
    # in different ways and stored in different places: one, a table of raw
    # JSON. another, a table representing references. I got confused during the
    # data migration and inlined the raw blobs while also preserving the table
    # of parsed references. I may rename the refs column to reflect its more
    # "raw" nature but even so, I kind of prefer exposing refs as citations.
    citations: list[ReleaseRefSchema] = []

    class Meta:
        model = m.Release
        fields = COMMON_ENTITY_FIELDS + ["title", "original_title", "subtitle", "release_type",
                                         "release_stage", "release_date", "release_year",
                                         "volume", "issue", "pages", "number", "version",
                                         "publisher", "language", "license_slug",
                                         "withdrawn_status", "refs",]
        fields_optional = COMMON_ENTITY_OPTIONAL_FIELDS


# TODO annoying name thing
class WebcaptureCDXSchema(ModelSchema):
    webcapture_id: UUID | None

    class Meta:
        model = m.WebcaptureCDX
        fields = ["surt", "captured", "url", "mimetype", "status_code",
                  "sha1", "sha256", "size_bytes"]


# TODO annoying name thing
class WebcaptureURLSchema(ModelSchema):
    webcapture_id: UUID | None

    class Meta:
        model = m.WebcaptureURL
        fields = ["url", "rel"]


# TODO annoying name thing
class WebcaptureSchema(ModelSchema):
    release_id: UUID
    urls: list[WebcaptureURLSchema] = []
    cdx_lines: list[WebcaptureCDXSchema] = []

    class Meta:
        model = m.Webcapture
        fields = COMMON_ENTITY_FIELDS + ["original_url", "captured"]
        fields_optional = COMMON_ENTITY_OPTIONAL_FIELDS


ContainerSchema = create_schema(m.Container,
                                fields=COMMON_ENTITY_FIELDS
                                + ["name", "container_type", "publisher",
                                   "issnl", "issne", "issnp", "wikidata_qid",],
                                optional_fields=COMMON_ENTITY_OPTIONAL_FIELDS)

WorkSchema = create_schema(m.Work, fields=COMMON_ENTITY_FIELDS,
                           optional_fields=COMMON_ENTITY_OPTIONAL_FIELDS)

CreatorSchema = create_schema(m.Creator, fields=COMMON_ENTITY_FIELDS
                              + ["display_name", "given_name", "surname",
                                 "orcid"],
                              optional_fields=COMMON_ENTITY_OPTIONAL_FIELDS)


class FileURLSchema(ModelSchema):
    file_id: UUID

    class Meta:
        model = m.FileURL
        fields = ["rel", "url"]


class FileSchema(ModelSchema):
    releases: list[ReleaseSchema] = []
    urls: list[FileURLSchema] = []

    class Meta:
        model = m.File
        fields = COMMON_ENTITY_FIELDS + ["size_bytes", "sha1", "sha256", "md5",
                                         "mimetype"]
        fields_optional = COMMON_ENTITY_OPTIONAL_FIELDS


type EntitySchema = ReleaseSchema\
        | CreatorSchema | ContainerSchema\
        | WorkSchema | FileSchema | WebcaptureSchema


@v2api.api_operation(["HEAD", "GET"], "/health")
def status(request) -> HttpResponse:
    # ensure db connection is ok, return 200
    # test id 855c8fa7-3b78-4652-88b9-f37d226c3139
    try:
        m.Release.objects.get(id="855c8fa7-3b78-4652-88b9-f37d226c3139")
    except m.Release.DoesNotExist:
        pass
    return HttpResponse(status=HTTPStatus.OK)

# Container routes


@v2api.get("/container/lookup")
def lookup_container(request, lookup: Query[ContainerLookup]) -> ContainerSchema:
    """Look up a container using an external ID. If multiple containers match
    the ID, an arbitrary one is returned."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return ContainerSchema.from_orm(get_object_or_404(m.Container, id=ident))

    cs = m.Container.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(cs) == 0:
        raise Http404(f"no container found with {lookup.id_type} of {lookup.id_value}")
    return ContainerSchema.from_orm(cs[0])


@v2api.get("/container/{ident}")
def get_container(request, ident: UUID) -> ContainerSchema:
    """Get a single container by its ID."""
    return ContainerSchema.from_orm(get_object_or_404(m.Container, id=ident))


@v2api.get("/container/{ident}/releases", response=list[ReleaseSchema])
@paginate
def get_container_releases(request, ident: UUID) -> list[ReleaseSchema]:
    """Get all releases for a given container ID."""
    return [ReleaseSchema.from_orm(r) for r in m.Release.objects.filter(container__id=ident)]


@v2api.post("/container", auth=api_auth)
def create_container(request, container_in: ContainerSchema) -> HttpResponse:
    """Create a new container."""
    cs = m.Container.objects.filter(id=container_in.id)
    if len(cs) != 0:
        return v2api.create_response(request,
                                     f"container with id {container_in.id} already exists",
                                     status=HTTPStatus.BAD_REQUEST)
    m.Container(**container_in.dict()).save()
    return v2api.create_response(request, "container created", status=HTTPStatus.CREATED)


@v2api.post("/containers", auth=api_auth)
def bulk_create_containers(request, containers_in: list[ContainerSchema]) -> HttpResponse:
    """Bulk create a list of containers. Functionally equivalent to calling
    POST /container repeatedly."""
    m.Container.objects.bulk_create([m.Container(**cin.dict()) for cin in containers_in])
    return v2api.create_response(request, "containers created", status=HTTPStatus.CREATED)


@v2api.put("/container", auth=api_auth)
def update_container(request, container_in: ContainerSchema) -> HttpResponse:
    """
    Replace a container entity wholesale. Must specify entire content of
    entity; not a patch operation. Creates the entity if it doesn't yet exist.
    201 if a new release was created; 200 otherwise.
    """
    code = HTTPStatus.OK
    es = m.Container.objects.filter(id=container_in.id)
    entity = None

    if len(es) == 0:
        code = HTTPStatus.CREATED
        entity = m.Container(**container_in.dict())
    else:
        entity = es[0]
        for attr, value in container_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "release replaced with new content", status=code)


@v2api.delete("/container/{ident}", auth=api_auth)
def delete_container(request, ident: UUID) -> ContainerSchema:
    """Delete the container with a given ID."""
    c = get_object_or_404(m.Container, id=ident)
    out = ContainerSchema.from_orm(c)
    c.delete()
    return out

# Release routes


@v2api.get("/release/lookup")
def lookup_release(request, lookup: Query[ReleaseLookup]) -> ReleaseSchema:
    """Look up a release using an external ID. If multiple releases match the
    ID, an arbitrary one is returned."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return ReleaseSchema.from_orm(get_object_or_404(m.Release, id=ident))

    rs = m.Release.objects.filter(**{
        "extids__id_type": lookup.id_type,
        "extids__id_value": lookup.id_value})
    if len(rs) == 0:
        raise Http404(
                f"no release found with {lookup.id_type} of {lookup.id_value}")
    return ReleaseSchema.from_orm(rs[0])


@v2api.get("/release/lookup/fulltext")
def fulltext(request, lookup: Query[ReleaseLookup]) -> HttpResponse:
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return ReleaseSchema.from_orm(get_object_or_404(m.Release, id=ident))

    rs = m.Release.objects.filter(**{
        "extids__id_type": lookup.id_type,
        "extids__id_value": lookup.id_value})
    if len(rs) == 0:
        raise Http404(
                f"no release found with {lookup.id_type} of {lookup.id_value}")

    files = m.File.objects.filter(releasefile__release_id=rs[0].id)
    wayback_url = ""
    webarchive_url = ""
    other_url = ""
    for f in files:
        for u in f.urls.all():
            if "web.archive.org" in u.url:
                wayback_url = u.url
            elif u.rel == "webarchive":
                webarchive_url = u.url
            else:
                other_url = u.url

    url = ""
    if wayback_url != "":
        url = wayback_url
    elif webarchive_url != "":
        url = webarchive_url
    elif other_url != "":
        url = other_url

    if url == "":
        raise Http404(f"no fulltext for {lookup.id_type}:{lookup.id_value}")

    return HttpResponseRedirect(url)


@v2api.get("/release/{ident}")
def get_release(request, ident: UUID) -> ReleaseSchema:
    """Get a single release by its ID."""
    return ReleaseSchema.from_orm(get_object_or_404(m.Release, id=ident))


@v2api.post("/release", auth=api_auth)
def create_release(request, release_in: ReleaseSchema) -> HttpResponse:
    """Create a new release."""
    data = release_in.dict()
    extids = data.pop("extids")
    contribs = data.pop("contribs")
    abstracts = data.pop("abstracts")
    citations = data.pop("citations")
    with transaction.atomic():
        rs = m.Release.objects.select_for_update().filter(id=release_in.id)
        if len(rs) != 0:
            return v2api.create_response(request,
                                         f"release with id {release_in.id} already exists",
                                         status=HTTPStatus.UNPROCESSABLE_ENTITY)
        work_id = release_in.work_id
        if work_id is None:
            work = m.Work()
            work.save()
            work_id = work.id

        r = m.Release(**data | {"work_id": work_id})
        r.save()
        m.ReleaseExtId.objects.bulk_create([m.ReleaseExtId(**ext_id | {"release_id": r.id})
                                            for ext_id in extids])
        m.ReleaseContrib.objects.bulk_create([m.ReleaseContrib(**c | {"release_id": r.id})
                                              for c in contribs])
        m.ReleaseAbstract.objects.bulk_create([m.ReleaseAbstract(**a | {"release_id": r.id})
                                              for a in abstracts])
        m.ReleaseRef.objects.bulk_create([m.ReleaseRef(**c | {"release_id": r.id})
                                          for c in citations])

    return v2api.create_response(request, "release created", status=HTTPStatus.CREATED)


@v2api.get("/release/{ident}/container")
def get_release_container(request, ident: UUID) -> ContainerSchema:
    """Get a release's container (ie, journal)"""
    cs = m.Container.objects.filter(release__id=ident)
    if len(cs) == 0:
        raise Http404(f"release {ident} has no associated container")
    return ContainerSchema.from_orm(cs[0])


@v2api.get("/release/{ident}/work")
def get_release_work(request, ident: UUID) -> WorkSchema:
    """Get a the work that represents the platonic version of this release."""
    ws = m.Container.objects.filter(release__id=ident)
    # do not need to check length; work_id is required in schema
    return WorkSchema.from_orm(ws[0])


@v2api.get("/release/{ident}/files", response=list[FileSchema])
@paginate
def get_release_files(request, ident: UUID) -> list[FileSchema]:
    return [FileSchema.from_orm(e)
            for e
            in m.File.objects.filter(releasefile__release_id=ident)]


@v2api.get("/release/{ident}/contribs", response=list[ReleaseContribSchema])
@paginate
def get_release_contribs(request, ident: UUID) -> list[ReleaseContribSchema]:
    """Get a list of contributions to a given release; for example, authors.
    Some contributions will feature a creator_id that can be used to select
    richer information about a contribution (eg, orcid); many contributions
    will just be raw names pulled from a paper's author list, however."""
    return [ReleaseContribSchema.from_orm(rc)
            for rc in m.ReleaseContrib.objects.filter(release_id=ident)]


@v2api.get("/release/{ident}/webcaptures", response=list[WebcaptureSchema])
@paginate
def get_release_webcaptures(request, ident: UUID) -> list[WebcaptureSchema]:
    return [WebcaptureSchema.from_orm(wc)
            for wc
            in m.Webcapture.objects.filter(release_id=ident)]


@v2api.delete("/release/{ident}", auth=api_auth)
def delete_release(request, ident: UUID) -> ReleaseSchema:
    """Delete the release with a given ID."""
    r = get_object_or_404(m.Release, id=ident)
    out = ReleaseSchema.from_orm(r)
    r.delete()
    return out


@v2api.put("/release", auth=api_auth)
def update_release(request, release_in: ReleaseSchema) -> HttpResponse:
    """
    Replace a release entity wholesale. Must specify entire content of
    entity; not a patch operation. Creates the entity if it doesn't yet exist.
    201 if a new release was created; 200 otherwise.

    If no work_id is specified for a release that does not yet exist, one will be created.

    If no work_id is specified for a release that does exist, 422 is returned.
    """
    code = HTTPStatus.OK
    es = m.Release.objects.filter(id=release_in.id)
    data = release_in.dict()
    extids = data.pop("extids")
    contribs = data.pop("contribs")
    abstracts = data.pop("abstracts")
    citations = data.pop("citations")

    with transaction.atomic():
        if len(es) == 0:
            return create_release(request, release_in)

        if data.get("work_id") is None:
            return v2api.create_response(request,
                                         "cannot unset a release's work",
                                         status=HTTPStatus.UNPROCESSABLE_CONTENT)

        entity = es[0]
        for attr, value in data.items():
            setattr(entity, attr, value)

        entity.save()
        entity.extids.all().delete()
        m.ReleaseExtId.objects.bulk_create([m.ReleaseExtId(**extid|{"release_id":entity.id})
                                            for extid in extids])
        entity.contribs.all().delete()
        m.ReleaseContrib.objects.bulk_create([m.ReleaseContrib(**contrib|{"release_id":entity.id})
                                              for contrib in contribs])

        entity.abstracts.all().delete()
        m.ReleaseAbstract.objects.bulk_create([m.ReleaseAbstract(**a|{"release_id":entity.id})
                                               for a in abstracts])
        entity.citations.all().delete()
        m.ReleaseRef.objects.bulk_create([m.ReleaseRef(**c|{"release_id":entity.id})
                                          for c in citations])

    return v2api.create_response(request, "release replaced with new content", status=code)


@v2api.post("/releases", auth=api_auth)
def bulk_create_releases(request, releases_in: list[ReleaseSchema]) -> HttpResponse:
    """Bulk create a list of releases. Functionally equivalent to calling
    POST /release repeatedly."""

    release_kwargs = []
    extids = []
    contribs = []
    abstracts = []
    citations = []
    with transaction.atomic():
        for rs in releases_in:
            data = rs.dict()
            extids = [ext_id | {"release_id": data["id"]} for ext_id in data.pop("extids")]
            contribs = [contrib | {"release_id": data["id"]} for contrib in data.pop("contribs")]
            abstracts = [a | {"release_id": data["id"]} for a in data.pop("abstracts")]
            citations = [c | {"release_id": data["id"]} for c in data.pop("citations")]
            if data.get("work_id") is None:
                work = m.Work()
                work.save()
                data["work_id"] = work.id
            release_kwargs.append(data)

        m.Release.objects.bulk_create([m.Release(**kw) for kw in release_kwargs])
        m.ReleaseExtId.objects.bulk_create([m.ReleaseExtId(**ext_id) for ext_id in extids])
        m.ReleaseContrib.objects.bulk_create([m.ReleaseContrib(**c) for c in contribs])
        m.ReleaseAbstract.objects.bulk_create([m.ReleaseAbstract(**a) for a in abstracts])
        m.ReleaseRef.objects.bulk_create([m.ReleaseRef(**c) for c in citations])

    return v2api.create_response(request, "releases created", status=HTTPStatus.CREATED)


# Work routes

@v2api.get("/work/lookup")
def lookup_work(request, lookup: Query[LegacyLookup]) -> WorkSchema:
    """Lookup a work by metadata other than its UUID"""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return WorkSchema.from_orm(get_object_or_404(m.Work, id=ident))
    else:
        raise NotImplementedError()


@v2api.get("/work/{ident}")
def get_work(request, ident: UUID) -> WorkSchema:
    """Get a work (collection of releases) by its ID"""
    return WorkSchema.from_orm(get_object_or_404(m.Work, id=ident))


@v2api.get("/work/{ident}/releases", response=list[ReleaseSchema])
@paginate
def get_work_releases(request, ident: UUID) -> list[ReleaseSchema]:
    """Get all releases associated with a work's ID"""
    return [ReleaseSchema.from_orm(r) for r in m.Release.objects.filter(work_id=ident)]

@v2api.post("/work", auth=api_auth, include_in_schema=False)
def create_work(request) -> HttpResponse:
    return v2api.create_response(request, "create not supported for works; works are created via releases", status=HTTPStatus.METHOD_NOT_ALLOWED)


@v2api.post("/works", auth=api_auth, include_in_schema=False)
def bulk_create_works(request) -> HttpResponse:
    return v2api.create_response(request, "bulk create not supported for works; works are created via releases", status=HTTPStatus.METHOD_NOT_ALLOWED)


@v2api.delete("/work/{ident}", auth=api_auth)
def delete_work(request, ident: UUID) -> WorkSchema:
    """Delete a work by its ID"""
    entity = get_object_or_404(m.Work, id=ident)
    out = WorkSchema.from_orm(entity)
    entity.delete()
    return out


@v2api.put("/work", auth=api_auth)
def update_work(request, work_in: WorkSchema) -> HttpResponse:
    """Replace a work record wholesale."""
    code = HTTPStatus.OK
    es = m.Work.objects.filter(id=work_in.id)
    entity = None

    if len(es) == 0:
        code = HTTPStatus.CREATED
        entity = m.Work(**work_in.dict())
    else:
        entity = es[0]
        for attr, value in work_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "work replaced with new content", status=code)


# Creator routes

@v2api.get("/creator/lookup")
def lookup_creator(request, lookup: Query[CreatorLookup]) -> CreatorSchema:
    """Look up a creator using an external ID. If multiple
    creators match the ID, an arbitrary one is returned."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return CreatorSchema.from_orm(get_object_or_404(m.Creator, id=ident))

    es = m.Creator.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(es) == 0:
        raise Http404(f"no creator found with {lookup.id_type} of {lookup.id_value}")
    return CreatorSchema.from_orm(es[0])


@v2api.get("/creator/{ident}/releases", response=list[ReleaseSchema])
@paginate
def get_creator_releases(request, ident: UUID) -> list[ReleaseSchema]:
    """Get all the releases associated with a given creator. Note that for many
    releases, their authors exist only as raw contribs and do not have creator
    records."""
    return [ReleaseSchema.from_orm(r) for r in m.Release.objects.filter(
            contribs__creator_id=ident)]


@v2api.get("/creator/{ident}")
def get_creator(request, ident: UUID) -> CreatorSchema:
    return CreatorSchema.from_orm(get_object_or_404(m.Creator, id=ident))


@v2api.post("/creator", auth=api_auth)
def create_creator(request, creator_in: CreatorSchema) -> HttpResponse:
    """Create a new creator."""
    es = m.Creator.objects.filter(id=creator_in.id)
    if len(es) != 0:
        return v2api.create_response(request,
                                     f"creator with id {creator_in.id} already exists",
                                     status=HTTPStatus.BAD_REQUEST)
    m.Creator(**creator_in.dict()).save()
    return v2api.create_response(request, "creator created", status=HTTPStatus.CREATED)


@v2api.put("/creator", auth=api_auth)
def update_creator(request, creator_in: CreatorSchema) -> HttpResponse:
    """Replace a creator record wholesale."""
    code = HTTPStatus.OK
    es = m.Creator.objects.filter(id=creator_in.id)
    entity = None

    if len(es) == 0:
        code = HTTPStatus.CREATED
        entity = m.Creator(**creator_in.dict())
    else:
        entity = es[0]
        for attr, value in creator_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "creator replaced with new content", status=code)


@v2api.post("/creators", auth=api_auth)
def bulk_create_creators(request, creators_in: list[CreatorSchema]) -> HttpResponse:
    m.Creator.objects.bulk_create([m.Creator(**cin.dict()) for cin in creators_in])
    return v2api.create_response(request, "creators created", status=HTTPStatus.CREATED)

@v2api.delete("/creator/{ident}", auth=api_auth)
def delete_creator(request, ident: UUID) -> HttpResponse:
    """Delete a creator record. Note: does not delete associated releases."""
    entity = get_object_or_404(m.Creator, id=ident)
    out = CreatorSchema.from_orm(entity)
    entity.delete()
    return out

# TODO ref routes?

# File routes


@v2api.get("/file/lookup")
def lookup_file(request, lookup: Query[FileLookup]) -> FileSchema:
    """Look up a file by checksum."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return FileSchema.from_orm(get_object_or_404(m.File, id=ident))

    es = m.File.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(es) == 0:
        raise Http404(f"no file found with {lookup.id_type} of {lookup.id_value}")
    return FileSchema.from_orm(es[0])


@v2api.get("/file/{ident}/releases", response=list[ReleaseSchema])
@paginate
def get_file_releases(request, ident: UUID) -> list[ReleaseSchema]:
    """Get all the releases associated with a given file."""
    return [ReleaseSchema.from_orm(r) for r in m.Release.objects.filter(
        releasefile__file_id=ident)]


@v2api.get("/file/{ident}")
def get_file(request, ident: UUID) -> FileSchema:
    return FileSchema.from_orm(get_object_or_404(m.File, id=ident))


@v2api.post("/file", auth=api_auth)
def create_file(request, file_in: FileSchema) -> HttpResponse:
    """Create a new file. Note that file contents do not live in this database;
    we only track checksums and metadata here. File contents, if stored at all,
    live in blob storage elsewhere.

    Does not create releases in the file payload; those must already exist."""
    es = m.File.objects.filter(id=file_in.id)
    if len(es) != 0:
        return v2api.create_response(request,
                                     f"file with id {file_in.id} already exists",
                                     status=HTTPStatus.BAD_REQUEST)
    data = file_in.dict()
    urls = data.pop("urls")
    releases = data.pop("releases")  # TODO chotou...
    with transaction.atomic():
        f = m.File(**data)
        f.save()
        m.FileURL.objects.bulk_create(
                [m.FileURL(**url | {"file_id": f.id}) for url in urls])
        m.ReleaseFile.objects.bulk_create(
                [m.ReleaseFile(release_id=r["id"], file_id=f.id) for r in releases])
    return v2api.create_response(request, "file created", status=HTTPStatus.CREATED)


@v2api.put("/file", auth=api_auth)
def update_file(request, file_in: FileSchema) -> HttpResponse:
    """Replace a file record wholesale."""
    code = HTTPStatus.OK
    es = m.File.objects.filter(id=file_in.id)

    data = file_in.dict()
    urls = data.pop("urls")
    releases = data.pop("releases")

    with transaction.atomic():
        entity = None
        if len(es) == 0:
            code = HTTPStatus.CREATED
            entity = m.File(**data)
        else:
            entity = es[0]
            for attr, value in data.items():
                setattr(entity, attr, value)
        entity.save()
        entity.urls.all().delete()
        m.FileURL.objects.bulk_create([m.FileURL(**url|{"file_id": entity.id}) for url in urls])
        m.ReleaseFile.objects.filter(file_id=entity.id).delete()
        m.ReleaseFile.objects.bulk_create([m.ReleaseFile(file_id=entity.id, release_id=r["id"])
                                           for r in releases])

    return v2api.create_response(request, "file replaced with new content", status=code)


@v2api.post("/files", auth=api_auth)
def bulk_create_files(request, files_in: list[FileSchema]) -> HttpResponse:
    file_kwargs = []
    urls = []
    rfiles = []
    for fs in files_in:
        data = fs.dict()
        urls += [url|{"file_id":data["id"]} for url in data.pop("urls")]
        rfiles += [{"release_id": r["id"], "file_id": fs.id} for r in data.pop("releases")]
        file_kwargs.append(data)

    with transaction.atomic():
        m.File.objects.bulk_create([m.File(**kw) for kw in file_kwargs])
        m.FileURL.objects.bulk_create([m.FileURL(**url) for url in urls])
        m.ReleaseFile.objects.bulk_create(
                [m.ReleaseFile(**rfile) for rfile in rfiles])
    return v2api.create_response(request, "files created", status=HTTPStatus.CREATED)


@v2api.delete("/file/{ident}", auth=api_auth)
def delete_file(request, ident: UUID) -> HttpResponse:
    """Delete a file record. Does not delete associated releases. Actual
    file contents will continue to live in blob storage."""
    entity = get_object_or_404(m.File, id=ident)
    out = FileSchema.from_orm(entity)
    entity.delete()
    return out

# Webcapture routes


@v2api.get("/webcapture/lookup")
def lookup_webcapture(request, lookup: Query[LegacyLookup]) -> WebcaptureSchema:
    """Look up a webcapture by its legacy ID."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return WebcaptureSchema.from_orm(get_object_or_404(m.Webcapture, id=ident))
    else:
        raise NotImplementedError()


@v2api.get("/webcapture/{ident}/release")
def get_webcapture_release(request, ident: UUID) -> ReleaseSchema:
    return ReleaseSchema.from_orm(get_object_or_404(m.Webcapture, id=ident).release)


@v2api.get("/webcapture/{ident}")
def get_webcapture(request, ident: UUID) -> WebcaptureSchema:
    return WebcaptureSchema.from_orm(get_object_or_404(m.Webcapture, id=ident))


@v2api.post("/webcapture", auth=api_auth)
def create_webcapture(request, webcapture_in: WebcaptureSchema) -> HttpResponse:
    es = m.Webcapture.objects.filter(id=webcapture_in.id)
    if len(es) != 0:
        return v2api.create_response(request,
                                     f"webcapture with id {webcapture_in.id} already exists",
                                     status=HTTPStatus.BAD_REQUEST)
    data = webcapture_in.dict()
    urls = data.pop("urls")
    cdx_lines = data.pop("cdx_lines")
    with transaction.atomic():
        wc = m.Webcapture(**data)
        wc.save()
        m.WebcaptureURL.objects.bulk_create(
                [m.WebcaptureURL(**url|{"webcapture_id": wc.id}) for url in urls])
        m.WebcaptureCDX.objects.bulk_create(
                [m.WebcaptureCDX(**line|{"webcapture_id":wc.id}) for line in cdx_lines])
    return v2api.create_response(request, "webcapture created", status=HTTPStatus.CREATED)


@v2api.post("/webcaptures", auth=api_auth)
def bulk_create_webcaptures(request, webcaptures_in: list[WebcaptureSchema]) -> HttpResponse:
    webcapture_kwargs = []
    urls = []
    cdx_lines = []
    for wcs in webcaptures_in:
        data = wcs.dict()
        urls += [url|{"webcapture_id":data["id"]} for url in data.pop("urls")]
        cdx_lines += [line|{"webcapture_id":data["id"]} for line in data.pop("cdx_lines")]
        webcapture_kwargs.append(data)

    with transaction.atomic():
        m.Webcapture.objects.bulk_create([m.Webcapture(**kw) for kw in webcapture_kwargs])
        m.WebcaptureURL.objects.bulk_create([m.WebcaptureURL(**url) for url in urls])
        m.WebcaptureCDX.objects.bulk_create([m.WebcaptureCDX(**line) for line in cdx_lines])

    return v2api.create_response(request, "webcaptures created", status=HTTPStatus.CREATED)


@v2api.put("/webcapture", auth=api_auth)
def update_webcapture(request, webcapture_in:WebcaptureSchema) -> HttpResponse:
    code = HTTPStatus.OK
    es = m.Webcapture.objects.filter(id=webcapture_in.id)

    data = webcapture_in.dict()
    urls = data.pop("urls")
    cdx_lines = data.pop("cdx_lines")

    with transaction.atomic():
        entity = None
        if len(es) == 0:
            code = HTTPStatus.CREATED
            entity = m.Webcapture(**data)
        else:
            entity = es[0]
            for attr, value in data.items():
                setattr(entity, attr, value)
        entity.save()
        entity.urls.all().delete()
        m.WebcaptureURL.objects.bulk_create([m.WebcaptureURL(**url|{"webcapture_id": entity.id})
                                             for url in urls])
        entity.cdx_lines.all().delete()
        m.WebcaptureCDX.objects.bulk_create([m.WebcaptureCDX(**line|{"webcapture_id":entity.id})
                                             for line in cdx_lines])

    return v2api.create_response(request, "webcapture replaced with new content", status=code)


@v2api.delete("/webcapture/{ident}", auth=api_auth)
def delete_webcapture(request, ident: UUID) -> WebcaptureSchema:
    entity = get_object_or_404(m.Webcapture, id=ident)
    out = WebcaptureSchema.from_orm(entity)
    entity.delete()
    return out

# Changelog routes


class ChangelogQuery(pydantic.BaseModel):
    start: Annotated[datetime.date, pydantic.Field(default_factory=datetime.date.today)]
    window: datetime.timedelta | None = datetime.timedelta(days=1)

    @pydantic.field_validator("window", mode="after")
    @classmethod
    def max_window(cls, value: datetime.timedelta) -> datetime.timedelta:
        if value > datetime.timedelta(days=30):
            raise ValueError("max window size is 30d")
        return value


def changelog(cq: ChangelogQuery, model: m.Entity, es: EntitySchema) -> list[EntitySchema]:
    start_dt = datetime.datetime.combine(
            cq.start,
            datetime.time(tzinfo=ZoneInfo("UTC")))

    return [es.from_orm(r)
            for r in model.objects.filter(
                updated__range=[
                    start_dt-cq.window,
                    start_dt+datetime.timedelta(days=1)]).order_by("-updated")]


@v2api.get("/changelog/releases", response=list[ReleaseSchema])
@paginate
def release_changelog(request, cq: Query[ChangelogQuery]) -> list[ReleaseSchema]:
    """
    Get a list of releases sorted by updated date. By default, returns releases
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see releases from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.Release, ReleaseSchema)


@v2api.get("/changelog/creators", response=list[CreatorSchema])
@paginate
def creator_changelog(request, cq: Query[ChangelogQuery]) -> list[CreatorSchema]:
    """
    Get a list of creators sorted by updated date. By default, returns creators
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see creators from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.Creator, CreatorSchema)


@v2api.get("/changelog/containers", response=list[ContainerSchema])
@paginate
def container_changelog(request, cq: Query[ChangelogQuery]) -> list[ContainerSchema]:
    """
    Get a list of containers sorted by updated date. By default, returns containers
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see containers from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.Container, ContainerSchema)


@v2api.get("/changelog/works", response=list[WorkSchema])
@paginate
def work_changelog(request, cq: Query[ChangelogQuery]) -> list[WorkSchema]:
    """
    Get a list of works sorted by updated date. By default, returns works
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see works from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.Work, WorkSchema)


@v2api.get("/changelog/files", response=list[FileSchema])
@paginate
def file_changelog(request, cq: Query[ChangelogQuery]) -> list[FileSchema]:
    """
    Get a list of files sorted by updated date. By default, returns files
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see files from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.File, FileSchema)


@v2api.get("/changelog/webcaptures", response=list[WebcaptureSchema])
@paginate
def webcapture_changelog(request, cq: Query[ChangelogQuery]) -> list[WebcaptureSchema]:
    """
    Get a list of webcaptures sorted by updated date. By default, returns webcaptures
    updated on the current day. the start argument moves the query's window to
    the specified day (eg, 2025-04-01). The window argument specifices the
    number of days to query updates for in the format "1d" and is subtracted
    from the start day.

    For example, to see webcaptures from the month of April: ?start=2025-05-01&window=30d

    The maximum value for window is 30d.

    NB: all date handling assumes UTC.
    """
    return changelog(cq, m.Webcapture, WebcaptureSchema)


@v2api.exception_handler(Http404)
def not_found(request, exc):
    return v2api.create_response(
            request,
            {"message": str(exc)},
            status=404)

# Fileset routes

# TODO I'm punting on these.
