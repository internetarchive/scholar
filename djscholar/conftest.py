import os

# fcapi/test_views.py builds a ninja TestClient at import time, which claims the
# default NinjaAPI urls_namespace for the whole process. Any test that then
# exercises the real URLconf (ftsearch/test_verify.py does, via the Django test
# client) makes ninja re-validate on `v2api.urls` and raise on the duplicate.
# NINJA_SKIP_REGISTRY is ninja's own escape hatch for exactly this.
os.environ.setdefault("NINJA_SKIP_REGISTRY", "1")
