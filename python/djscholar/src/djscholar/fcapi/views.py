from typing import List, Optional

from ninja import NinjaAPI, ModelSchema
from ninja_apikey.security import APIKeyAuth

import djscholar.fcapi.models as models

v2api = NinjaAPI()
# NB: uses X-API-Key header. use admin to create keys.
apiAuth = APIKeyAuth()

# TODO ModelSchema for return types
# TODO filter hidden things

def lookup_entity(entiy_type: str, id_type: str, id_value: str) -> Optional[models.Entity]:
    # TODO
    return None

def get_entity(entity_type: str, ident: str) -> Optional[models.Entity]:
    # TODO
    return None

def delete_entity(entity_type: str, ident: str) -> Optional[models.Entity]:
    return None

# Container routes

COMMON_ENTITY_FIELDS = ["id", "created", "updated", "source", "extra"]

class ContainerSchema(ModelSchema):
    # TODO consider adding a release_count field
    class Meta:
        model = models.Container
        fields = COMMON_ENTITY_FIELDS + ["name", "container_type", "publisher", "issnl",
                                         "issne", "issnp", "wikidata_qid",]

@v2api.get("/container/lookup")
def container_lookup(request, id_type: str, id_value: str) -> Optional[ContainerSchema]:
    cs = models.Container.objects.filter(**{id_type: id_value})
    if len(cs) == 0:
        return None
    return ContainerSchema.from_orm(cs[0])

@v2api.get("/container/{ident}")
def container_get(request, ident: str) -> Optional[models.Container]:
    # TODO
    return {}

@v2api.get("/container/{ident}/releases")
def container_releases(request, ident: str) -> List[models.Release]:
    # TODO
    return []

@v2api.delete("/container", auth=apiAuth)
def container_delete(ident: str) -> Optional[models.Container]:
    # TODO
    return None

# TODO decide on patch updates vs overwrites (starting with patch updates)
@v2api.post("/container", auth=apiAuth)
def container_update(ident: str) -> Optional[models.Container]:
    # TODO
    return None

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
