import uuid
from typing import List, Literal

from django.http import HttpResponse, Http404
from django.shortcuts import get_object_or_404
from ninja import NinjaAPI, Query, Schema, ModelSchema
from ninja.orm import create_schema
from ninja_apikey.security import APIKeyAuth

from djscholar.fcapi.models import Container, Release, RELEASE_EXT_ID_TYPES, Work

v2api = NinjaAPI()
# NB: uses X-API-Key header. use admin to create keys.
apiAuth = APIKeyAuth()

# TODO filter hidden things
# TODO consider generalizing route implementations if it doesn't make
# signatures too hideous / doesn't break doc generation
# TODO pagination
# TODO support nested containers and works (and possibly other types) during creation/update; possibly getting

COMMON_ENTITY_FIELDS = ["id", "created", "updated", "source", "extra"]

# In/Out schemas

class ContainerLookup(Schema):
    id_type: Literal["issnl", "issne", "issnp", "wikidata_qid"]
    id_value: str


class ReleaseLookup(Schema):
    id_type: Literal[*[t[0] for t in RELEASE_EXT_ID_TYPES]]
    id_value: str


ContainerSchema = create_schema(Container,
                                fields=COMMON_ENTITY_FIELDS\
                                       + ["name", "container_type", "publisher", "issnl",
                                          "issne", "issnp", "wikidata_qid",])
class ReleaseSchema(ModelSchema):
    work_id: uuid.UUID
    container_id: uuid.UUID

    class Meta:
        model = Release
        fields = COMMON_ENTITY_FIELDS + ["title", "original_title", "subtitle", "release_type",
                                         "release_stage", "release_date", "release_year",
                                         "volume", "issue", "pages", "number", "version",
                                         "publisher", "language", "license_slug",
                                         "withdrawn_status", "refs",]

WorkSchema = create_schema(Work, fields=COMMON_ENTITY_FIELDS)


# Container routes

@v2api.get("/container/lookup")
def lookup_container(request, lookup: Query[ContainerLookup]) -> ContainerSchema:
    """Look up a container using an external ID. If multiple containers match
    the ID, an arbitrary one is returned."""
    cs = Container.objects.filter(**{lookup.id_type: lookup.id_value})
    if len(cs) == 0:
        raise Http404(f"no container found with {lookup.id_type} of {lookup.id_value}")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/container/{ident}")
def get_container(request, ident: str) -> ContainerSchema:
    """Get a single container by its ID."""
    # TODO handle legacy idents
    return ContainerSchema.from_orm(get_object_or_404(Container, id=ident))

@v2api.get("/container/{ident}/releases")
def get_container_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all releases for a given container ID."""
    # TODO handle legacy idents
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
    # TODO handle legacy idents
    c = get_object_or_404(Container, id=ident)
    out = ContainerSchema.from_orm(c)
    c.delete()
    return out

# Release routes

@v2api.get("/release/lookup")
def lookup_release(request, lookup: Query[ReleaseLookup]) -> ReleaseSchema:
    """Look up a release using an external ID. If multiple releases match the
    ID, an arbitrary one is returned."""
    rs = Release.objects.filter(**{
        "releaseextid__id_type": lookup.id_type,
        "releaseextid__id_value": lookup.id_value})
    if len(rs) == 0:
        raise Http404(f"no release found with {lookup.id_type} of {lookup.id_value}")
    return ReleaseSchema.from_orm(rs[0])

@v2api.get("/release/{ident}")
def get_release(request, ident: str) -> ReleaseSchema:
    """Get a single release by its ID."""
    # TODO handle legacy idents
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
    # TODO handle legacy idents
    cs = Container.objects.filter(release__id=ident)
    if len(cs) == 0:
        raise Http404(f"release {ident} has no associated container")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/release/{ident}/work")
def get_release_work(request, ident: str) -> WorkSchema:
    """Get a the work that represents the platonic version of this release."""
    # TODO handle legacy idents
    ws = Container.objects.filter(release__id=ident)
    # do not need to check length; work_id is required in schema
    return WorkSchema.from_orm(ws[0])

@v2api.delete("/release/{ident}", auth=apiAuth)
def delete_release(request, ident: str) -> ReleaseSchema:
    """Delete the release with a given ID."""
    # TODO handle legacy idents
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

# TODO GET /release/{ident}/files
# TODO GET /release/{ident}/creators


# Work routes

# TODO get /work/{ident}/releases
# TODO delete /work/{ident}
# TODO put /work

# Creator routes

# TODO should support the creation of creators via release creation/update

# TODO get /creator/lookup
# TODO get /creator/{ident}
# TODO get /creator/releases
# TODO post /creator
# TODO put /creator
# TODO delete /creator
# TODO ref routes?

# File routes

# TODO get /file/lookup
# TODO get /file/{ident}
# TODO post /file
# TODO put /file
# TODO post /files
# TODO delete /file/{ident}

# Changelog routes

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
