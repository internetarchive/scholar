from django.http import HttpRequest, HttpResponse

from djscholar.fcapi.services import EntityHidden


class EntityHiddenMiddleware:
    """Serves a 410 page for any view that runs into a hidden entity.

    Service get()/lookup() calls raise EntityHidden for hidden (eg, taken down)
    entities. Views only catch EntityNotFound, so EntityHidden ends up here
    rather than every view handling it.
    """

    def __init__(self, get_response):
        self.get_response = get_response

    def __call__(self, request: HttpRequest) -> HttpResponse:
        return self.get_response(request)

    def process_exception(self, request: HttpRequest, exception: Exception) -> HttpResponse | None:
        if not isinstance(exception, EntityHidden):
            return None
        # imported here since fcweb.views imports a good deal at load time
        from djscholar.fcweb.views import _render
        return _render(request, "fcweb/gone.html",
                       {"entity_type": exception.entity_type}, status=410)
