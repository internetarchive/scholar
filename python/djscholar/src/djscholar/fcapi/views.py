from ninja import NinjaAPI

v2api = NinjaAPI()

@v2api.get("/lookup")
def lookup(request, entity_type: str, id_type: str, id_value: str) -> dict:
    return {"id": "123"}
