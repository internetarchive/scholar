from typing import List

from django.http import HttpResponse, Http404
from django.shortcuts import get_object_or_404
from ninja import NinjaAPI
from ninja.orm import create_schema
from ninja_apikey.security import APIKeyAuth

from djscholar.fcapi.models import Container, Release

v2api = NinjaAPI()
# NB: uses X-API-Key header. use admin to create keys.
apiAuth = APIKeyAuth()

# TODO filter hidden things
# TODO consider generalizing route implementations if it doesn't make
# signatures too hideous / doesn't break doc generation
# TODO pagination

COMMON_ENTITY_FIELDS = ["id", "created", "updated", "source", "extra"]

# In/Out schemas

ContainerSchema = create_schema(Container,
                                fields=COMMON_ENTITY_FIELDS\
                                       + ["name", "container_type", "publisher", "issnl",
                                          "issne", "issnp", "wikidata_qid",])

ReleaseSchema = create_schema(Release,
                              fields=COMMON_ENTITY_FIELDS\
                                      + ["work", "container", "title", "original_title",
                                         "subtitle", "release_type", "release_stage",
                                         "release_date", "release_year", "volume", "issue",
                                         "pages", "number", "version", "publisher", "language",
                                         "license_slug", "withdrawn_status", "refs",])


# Container routes

@v2api.get("/container/lookup")
def container_lookup(request, id_type: str, id_value: str) -> ContainerSchema:
    """Look up a container using an external ID. Valid ID types: issnl, issne,
    issnp, wikidata_qid. If multiple containers match the ID, an arbitrary one
    is returned."""
    cs = Container.objects.filter(**{id_type: id_value})
    if len(cs) == 0:
        raise Http404(f"no container found with {id_type} of {id_value}")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/container/{ident}")
def container_get(request, ident: str) -> ContainerSchema:
    """Get a single container by its ID."""
    # TODO handle legacy idents
    return ContainerSchema.from_orm(get_object_or_404(Container, id=ident))

@v2api.get("/container/{ident}/releases")
def container_releases(request, ident: str) -> List[ReleaseSchema]:
    """Get all releases for a given container ID."""
    # TODO handle legacy idents
    # TODO paginate
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(container__id=ident)]

@v2api.delete("/container/{ident}", auth=apiAuth)
def container_delete(request, ident: str) -> ContainerSchema:
    """Delete the container with a given ID."""
    # TODO handle legacy idents
    c = get_object_or_404(Container, id=ident)
    c.delete()
    return ContainerSchema.from_orm(c)

@v2api.post("/container", auth=apiAuth)
def container_create(request, container_in: ContainerSchema) -> HttpResponse:
    """Create a new container."""
    cs = Container.objects.filter(id=container_in.id)
    if len(cs) != 0:
        return v2api.create_response(request,
                                     f"container with id {container_in.id} already exists",
                                     status=400)
    Container(**container_in.dict()).save()
    return v2api.create_response(request, "container created", status=201)

@v2api.put("/container", auth=apiAuth)
def container_update(request, container_in: ContainerSchema) -> HttpResponse:
    """
    Replace a container entity wholesale. Must specify entire content of
    entity; not a patch operation. 404s if container does not yet exist.
    """
    in_dict = container_in.dict()
    c = get_object_or_404(Container, id=in_dict["id"])
    for attr, value in in_dict.items():
        setattr(c, attr, value)
    c.save()
    return v2api.create_response(request, "container created", status=200)

@v2api.post("/containers", auth=apiAuth)
def container_batch_create(request, containers_in: List[ContainerSchema]) -> HttpResponse:
    """Bulk create a list of containers. Functionally equivalent to calling
    POST /container repeatedly."""
    Container.objects.bulk_create([Container(**cin.dict()) for cin in containers_in])
    return v2api.create_response(request, "containers created", status=201)

# Release routes

# TODO

# Work routes

# TODO

# Creator routes

# TODO

# File routes

# TODO

# Fileset routes

# TODO

# Webcapture routes

# TODO

# Changelog routes

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
# batch create entity
# update entity (replace)
# delete entity
