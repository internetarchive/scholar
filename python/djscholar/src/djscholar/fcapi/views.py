import uuid
from typing import List, Literal, Optional

from django.http import HttpResponse, Http404
from django.shortcuts import get_object_or_404
from ninja import NinjaAPI, Query, Schema, ModelSchema
from ninja.orm import create_schema
from ninja_apikey.security import APIKeyAuth

from djscholar.fcapi.fcid import fcid2uuid
from djscholar.fcapi.models import Container, Creator, File, Release, ReleaseContrib, RELEASE_EXT_ID_TYPES, Work

v2api = NinjaAPI()
# NB: uses X-API-Key header. use admin to create keys.
apiAuth = APIKeyAuth()

# TODO filter hidden things
# TODO consider generalizing route implementations if it doesn't make
# signatures too hideous / doesn't break doc generation
# TODO pagination
# TODO support nested containers and works (and possibly other types) during creation/update; possibly getting
# TODO add legacy_ident lookup to all entity lookup routes

COMMON_ENTITY_FIELDS = ["id", "created", "updated", "source", "extra"]

# In/Out schemas

class ContainerLookup(Schema):
    id_type: Literal["issnl", "issne", "issnp", "wikidata_qid", "legacy_ident"]
    id_value: str


class CreatorLookup(Schema):
    id_type: Literal["orcid", "legacy_ident"]
    id_value: str


class FileLookup(Schema):
    id_type: Literal["sha1", "sha256", "md5", "legacy_ident"]
    id_value: str


class ReleaseLookup(Schema):
    id_type: Literal[*[t[0] for t in RELEASE_EXT_ID_TYPES] + ["legacy_ident"]]
    id_value: str


class WorkLookup(Schema):
    id_type: Literal["legacy_ident"]
    id_value: str


ContainerSchema = create_schema(Container,
                                fields=COMMON_ENTITY_FIELDS\
                                       + ["name", "container_type", "publisher", "issnl",
                                          "issne", "issnp", "wikidata_qid",])


class ReleaseSchema(ModelSchema):
    work_id: uuid.UUID
    container_id: Optional[uuid.UUID]

    class Meta:
        model = Release
        fields = COMMON_ENTITY_FIELDS + ["title", "original_title", "subtitle", "release_type",
                                         "release_stage", "release_date", "release_year",
                                         "volume", "issue", "pages", "number", "version",
                                         "publisher", "language", "license_slug",
                                         "withdrawn_status", "refs",]

WorkSchema = create_schema(Work, fields=COMMON_ENTITY_FIELDS)

CreatorSchema = create_schema(Creator, fields=COMMON_ENTITY_FIELDS+["display_name", "given_name",
                                                                    "surname", "orcid"])

class ReleaseContribSchema(ModelSchema):
    release_id: uuid.UUID
    creator_id: Optional[uuid.UUID]

    class Meta:
        model = ReleaseContrib
        fields = ["raw_name", "given_name", "surname", "role", "raw_affiliation",
                  "position", "extra"]

FileSchema = create_schema(File, fields=COMMON_ENTITY_FIELDS + ["size_bytes", "sha1", "sha256",
                                                                "md5", "mimetype"])

# Container routes

@v2api.get("/container/lookup")
def lookup_container(request, lookup: Query[ContainerLookup]) -> ContainerSchema:
    """Look up a container using an external ID. If multiple containers match
    the ID, an arbitrary one is returned."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return ContainerSchema.from_orm(get_object_or_404(Container, id=ident))

    cs = Container.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(cs) == 0:
        raise Http404(f"no container found with {lookup.id_type} of {lookup.id_value}")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/container/{ident}")
def get_container(request, ident: str) -> ContainerSchema:
    """Get a single container by its ID."""
    return ContainerSchema.from_orm(get_object_or_404(Container, id=ident))

@v2api.get("/container/{ident}/releases")
def get_container_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all releases for a given container ID."""
    # TODO paginate
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(container__id=ident)]


@v2api.post("/container", auth=apiAuth)
def create_container(request, container_in: ContainerSchema) -> HttpResponse:
    """Create a new container."""
    cs = Container.objects.filter(id=container_in.id)
    if len(cs) != 0:
        return v2api.create_response(request,
                                     f"container with id {container_in.id} already exists",
                                     status=400)
    Container(**container_in.dict()).save()
    return v2api.create_response(request, "container created", status=201)

@v2api.post("/containers", auth=apiAuth)
def bulk_create_containers(request, containers_in: List[ContainerSchema]) -> HttpResponse:
    """Bulk create a list of containers. Functionally equivalent to calling
    POST /container repeatedly."""
    Container.objects.bulk_create([Container(**cin.dict()) for cin in containers_in])
    return v2api.create_response(request, "containers created", status=201)

@v2api.put("/container", auth=apiAuth)
def update_container(request, container_in: ContainerSchema) -> HttpResponse:
    """
    Replace a container entity wholesale. Must specify entire content of
    entity; not a patch operation. Creates the entity if it doesn't yet exist.
    201 if a new release was created; 200 otherwise.
    """
    code = 200
    es = Container.objects.filter(id=container_in.id)
    entity = None

    if len(es) == 0:
        code = 201
        entity = Container(**container_in.dict())
    else:
        entity = es[0]
        for attr, value in container_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "release replaced with new content", status=code)

@v2api.delete("/container/{ident}", auth=apiAuth)
def delete_container(request, ident: str) -> ContainerSchema:
    """Delete the container with a given ID."""
    c = get_object_or_404(Container, id=ident)
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
        return ReleaseSchema.from_orm(get_object_or_404(Release, id=ident))

    rs = Release.objects.filter(**{
        "releaseextid__id_type": lookup.id_type,
        "releaseextid__id_value": lookup.id_value})
    if len(rs) == 0:
        raise Http404(f"no release found with {lookup.id_type} of {lookup.id_value}")
    return ReleaseSchema.from_orm(rs[0])

@v2api.get("/release/{ident}")
def get_release(request, ident: str) -> ReleaseSchema:
    """Get a single release by its ID."""
    return ReleaseSchema.from_orm(get_object_or_404(Release, id=ident))

@v2api.post("/release", auth=apiAuth)
def create_release(request, release_in: ReleaseSchema) -> HttpResponse:
    """Create a new release."""
    # TODO releases without works should have works created automagically
    rs = Release.objects.filter(id=release_in.id)
    if len(rs) != 0:
        return v2api.create_response(request,
                                     f"release with id {release_in.id} already exists",
                                     status=400)
    Release(**release_in.dict()).save()
    return v2api.create_response(request, "release created", status=201)

@v2api.get("/release/{ident}/container")
def get_release_container(request, ident: str) -> ContainerSchema:
    """Get a release's container (ie, journal)"""
    cs = Container.objects.filter(release__id=ident)
    if len(cs) == 0:
        raise Http404(f"release {ident} has no associated container")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/release/{ident}/work")
def get_release_work(request, ident: str) -> WorkSchema:
    """Get a the work that represents the platonic version of this release."""
    ws = Container.objects.filter(release__id=ident)
    # do not need to check length; work_id is required in schema
    return WorkSchema.from_orm(ws[0])

@v2api.get("/release/{ident}/files")
def get_release_files(request, ident: str) -> List[FileSchema]:
    return [FileSchema.from_orm(e) for e in File.objects.filter(releasefile__release_id=ident)]

@v2api.get("/release/{ident}/contribs")
def get_release_contribs(request, ident: str) -> List[CreatorSchema]:
    """Get a list of contributions to a given release; for example, authors.
    Some contributions will feature a creator_id that can be used to select
    richer information about a contribution (eg, orcid); many contributions
    will just be raw names pulled from a paper's author list, however."""
    return [ReleaseContribSchema.from_orm(rc)
            for rc in ReleaseContrib.objects.filter(release_id=ident)]

@v2api.delete("/release/{ident}", auth=apiAuth)
def delete_release(request, ident: str) -> ReleaseSchema:
    """Delete the release with a given ID."""
    r = get_object_or_404(Release, id=ident)
    out = ReleaseSchema.from_orm(r)
    r.delete()
    return out

@v2api.put("/release", auth=apiAuth)
def update_release(request, release_in: ReleaseSchema) -> HttpResponse:
    """
    Replace a release entity wholesale. Must specify entire content of
    entity; not a patch operation. Creates the entity if it doesn't yet exist.
    201 if a new release was created; 200 otherwise.
    """
    code = 200
    es = Release.objects.filter(id=release_in.id)
    entity = None

    if len(es) == 0:
        code = 201
        entity = Release(**release_in.dict())
    else:
        entity = es[0]
        for attr, value in release_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "release replaced with new content", status=code)

@v2api.post("/releases", auth=apiAuth)
def bulk_create_releases(request, releases_in: List[ReleaseSchema]) -> HttpResponse:
    """Bulk create a list of releases. Functionally equivalent to calling
    POST /release repeatedly."""
    # TODO releases without works should have works created automagically
    Release.objects.bulk_create([Release(**rin.dict()) for rin in releases_in])
    return v2api.create_response(request, "releases created", status=201)


# Work routes

@v2api.get("/work/lookup")
def lookup_work(request, lookup: Query[WorkLookup]) -> WorkSchema:
    """Lookup a work by metadata other than its UUID"""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return WorkSchema.from_orm(get_object_or_404(Work, id=ident))
    else:
        raise NotImplementedError()

@v2api.get("/work/{ident}")
def get_work(request, ident: str) -> WorkSchema:
    """Get a work (collection of releases) by its ID"""
    return WorkSchema.from_orm(get_object_or_404(Work, id=ident))

@v2api.get("/work/{ident}/releases")
def get_work_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all releases associated with a work's ID"""
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(work_id=ident)]

@v2api.delete("/work/{ident}", auth=apiAuth)
def delete_work(request, ident: str) -> WorkSchema:
    """Delete a work by its ID"""
    entity = get_object_or_404(Work, id=ident)
    out = WorkSchema.from_orm(entity)
    entity.delete()
    return out

@v2api.put("/work", auth=apiAuth)
def update_work(request, work_in: WorkSchema) -> HttpResponse:
    """Replace a work record wholesale."""
    code = 200
    es = Work.objects.filter(id=work_in.id)
    entity = None

    if len(es) == 0:
        code = 201
        entity = Work(**work_in.dict())
    else:
        entity = es[0]
        for attr, value in work_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "work replaced with new content", status=code)

# Creator routes

# TODO should support the creation of creators via release creation/update

@v2api.get("/creator/lookup")
def lookup_creator(request, lookup: Query[CreatorLookup]) -> CreatorSchema:
    """Look up a creator using an external ID. If multiple
    creators match the ID, an arbitrary one is returned."""
    if lookup.id_type == "legacy_ident":
        ident = fcid2uuid(lookup.id_value)
        return CreatorSchema.from_orm(get_object_or_404(Creator, id=ident))

    es = Creator.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(es) == 0:
        raise Http404(f"no creator found with {lookup.id_type} of {lookup.id_value}")
    return CreatorSchema.from_orm(es[0])

@v2api.get("/creator/{ident}/releases")
def get_creator_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all the releases associated with a given creator. Note that for many
    releases, their authors exist only as raw contribs and do not have creator
    records."""
    # TODO paginate
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(
        releasecontrib__creator_id=ident)]

@v2api.get("/creator/{ident}")
def get_creator(request, ident: str) -> CreatorSchema:
    return CreatorSchema.from_orm(get_object_or_404(Creator, id=ident))

@v2api.post("/creator", auth=apiAuth)
def create_creator(request, creator_in: CreatorSchema) -> HttpResponse:
    """Create a new creator."""
    es = Creator.objects.filter(id=creator_in.id)
    if len(es) != 0:
        return v2api.create_response(request,
                                     f"creator with id {creator_in.id} already exists",
                                     status=400)
    Creator(**creator_in.dict()).save()
    return v2api.create_response(request, "creator created", status=201)

@v2api.put("/creator", auth=apiAuth)
def update_creator(request, creator_in: CreatorSchema) -> HttpResponse:
    """Replace a creator record wholesale."""
    code = 200
    es = Creator.objects.filter(id=creator_in.id)
    entity = None

    if len(es) == 0:
        code = 201
        entity = Creator(**creator_in.dict())
    else:
        entity = es[0]
        for attr, value in creator_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "creator replaced with new content", status=code)

@v2api.post("/creators", auth=apiAuth)
def bulk_create_creators(request, creators_in: List[CreatorSchema]) -> HttpResponse:
    Creator.objects.bulk_create([Creator(**cin.dict()) for cin in creators_in])
    return v2api.create_response(request, "creators created", status=201)

@v2api.delete("/creator/{ident}", auth=apiAuth)
def delete_creator(request, ident: str) -> HttpResponse:
    """Delete a creator record. Note: does not delete associated releases."""
    entity = get_object_or_404(Creator, id=ident)
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
        return FileSchema.from_orm(get_object_or_404(File, id=ident))

    es = File.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(es) == 0:
        raise Http404(f"no file found with {lookup.id_type} of {lookup.id_value}")
    return FileSchema.from_orm(es[0])

@v2api.get("/file/{ident}/releases")
def get_file_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all the releases associated with a given file."""
    # TODO paginate
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(
        releasefile__file_id=ident)]

@v2api.get("/file/{ident}")
def get_file(request, ident: str) -> FileSchema:
    return FileSchema.from_orm(get_object_or_404(File, id=ident))

@v2api.post("/file", auth=apiAuth)
def create_file(request, file_in: FileSchema) -> HttpResponse:
    """Create a new file. Note that file contents do not live in this database;
    we only track checksums and metadata here. File contents, if stored at all,
    live in blob storage elsewhere."""
    es = File.objects.filter(id=file_in.id)
    if len(es) != 0:
        return v2api.create_response(request,
                                     f"file with id {file_in.id} already exists",
                                     status=400)
    File(**file_in.dict()).save()
    return v2api.create_response(request, "file created", status=201)

@v2api.put("/file", auth=apiAuth)
def update_file(request, file_in: FileSchema) -> HttpResponse:
    """Replace a file record wholesale."""
    code = 200
    es = File.objects.filter(id=file_in.id)
    entity = None

    if len(es) == 0:
        code = 201
        entity = File(**file_in.dict())
    else:
        entity = es[0]
        for attr, value in file_in.dict().items():
            setattr(entity, attr, value)

    entity.save()

    return v2api.create_response(request, "file replaced with new content", status=code)

@v2api.post("/files", auth=apiAuth)
def bulk_create_files(request, files_in: List[FileSchema]) -> HttpResponse:
    File.objects.bulk_create([File(**cin.dict()) for cin in files_in])
    return v2api.create_response(request, "files created", status=201)

@v2api.delete("/file/{ident}", auth=apiAuth)
def delete_file(request, ident: str) -> HttpResponse:
    """Delete a file record. Does not delete associated releases. Actual
    file contents will continue to live in blob storage."""
    entity = get_object_or_404(File, id=ident)
    out = FileSchema.from_orm(entity)
    entity.delete()
    return out

# Changelog routes

# Instead of a single /changelog, I am considering the ability to query for created or updated entities based on a time range (ie, "last 24 hours" or some other interval). I think this will end up being more useful than the current changelog system.

# TODO

# Fileset routes

# TODO

# Webcapture routes

# TODO

#### routes outline:

### reads

# lookup entity
# get entity
# get entity's children (ie, work releases)
# get entity's parent  (ie release container)

# child relationships:
# - works have releases
# - containers have releases
# - releases have files

# changelog
# changelog/{index}

### writes

# create entity
# bulk create entity
# update entity (replace)
# delete entity
