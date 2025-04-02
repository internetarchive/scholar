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

# TODO ModelSchema for return types
# TODO filter hidden things
# TODO consider generalizing route implementations if it doesn't make signatures too hideous / doesn't break doc generation
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
    cs = Container.objects.filter(**{id_type: id_value})
    if len(cs) == 0:
        raise Http404(f"no container found with {id_type} of {id_value}")
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/container/{ident}")
def container_get(request, ident: str) -> ContainerSchema:
    # TODO handle legacy idents
    return ContainerSchema.from_orm(get_object_or_404(Container, id=ident))

@v2api.get("/container/{ident}/releases")
def container_releases(request, ident: str) -> List[ReleaseSchema]:
    # TODO handle legacy idents
    return [ReleaseSchema.from_orm(r) for r in Release.objects.filter(container__id=ident)]

@v2api.delete("/container/{ident}", auth=apiAuth)
def container_delete(request, ident: str) -> ContainerSchema:
    # TODO handle legacy idents
    c = get_object_or_404(Container, id=ident)
    c.delete()
    return ContainerSchema.from_orm(c)

@v2api.post("/container", auth=apiAuth)
def container_create(request, container_in: ContainerSchema) -> HttpResponse:
    c = Container()
    for attr, value in container_in.dict().items():
        setattr(c, attr, value)
    c.save()

    return v2api.create_response(request, "container created", status=201)

@v2api.put("/container", auth=apiAuth)
def container_update(request, container_in: ContainerSchema) -> HttpResponse:
    """
    Replace a container entity wholesale. Must specify entire content of entity; not a patch operation.
    """
    in_dict = container_in.dict()
    c = get_object_or_404(Container, id=in_dict["id"])
    for attr, value in in_dict.items():
        setattr(c, attr, value)
    c.save()
    return v2api.create_response(request, "container created", status=200)

@v2api.post("/containers", auth=apiAuth)
def container_batch_create(request, containers_in: List[ContainerSchema]) -> HttpResponse:
    cs: List[Container] = []
    for cin in containers_in:
        c = Container()
        for attr, value in cin.dict():
            setattr(c, attr, value)
        cs.append(c)
    Container.objects.bulk_create(cs)

    return v2api.create_response(request, "containers created", status=201)

# Release routes

# Work routes

# 

# reads

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

# writes

# set of API keys with:
# - expiry
# - value
# - name

# create entity
# batch create entity
# update entity
# delete entity
