from functools import lru_cache
from typing import List, Optional

from ninja import NinjaAPI
from ninja.security import HttpBearer

import djscholar.fcapi.models as models

v2api = NinjaAPI()

@lru_cache
def get_auth_tokens() -> List[str]:
    return [t.token for t in models.AuthToken.objects.all()]

class AuthBearer(HttpBearer):
    def authenticate(self, request, token):
        if token == "" or len(token) < 43:
            return None
        valid_tokens = get_auth_tokens()
        return token in valid_tokens

# TODO ModelSchema for return types

def lookup_entity(entiy_type: str, id_type: str, id_value: str) -> Optional[models.Entity]:
    # TODO
    return None

def get_entity(entity_type: str, ident: str) -> Optional[models.Entity]:
    # TODO
    return None

def delete_entity(entity_type: str, ident: str) -> Optional[models.Entity]:
    return None

# Container routes

@v2api.get("/container/lookup")
def container_lookup(request, id_type: str, id_value: str) -> Optional[models.Container]:
    # TODO
    return None

@v2api.get("/container/{ident}")
def container_get(request, ident: str) -> Optional[models.Container]:
    # TODO
    return {}

@v2api.get("/container/{ident}/releases")
def container_releases(request, ident: str) -> List[models.Release]:
    # TODO
    return []

@v2api.delete("/container", auth=AuthBearer())
def container_delete(ident: str) -> Optional[models.Container]:
    # TODO
    return None

# TODO decide on patch updates vs overwrites (starting with patch updates)
@v2api.post("/container", auth=AuthBearer())
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
