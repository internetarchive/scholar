from ninja import NinjaAPI

v2api = NinjaAPI()

@v2api.get("/lookup/{entity_type}")
def lookup(request, entity_type: str, id_type: str, id_value: str) -> dict:
    return {"id": "123"}

# reads

# lookup entity
# get entity
# get entity's children (ie, work releases)
# get entity's parent  (ie release container)

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
