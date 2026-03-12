from django.http import HttpRequest, HttpResponse
from django.shortcuts import render


def webhealth(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def health(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def searchhealth(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def home(request: HttpRequest) -> HttpResponse:
    return render(request, "ftsearch/home.html")


def about(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def help(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def permalink(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def search(request: HttpRequest) -> HttpResponse:
    raise NotImplementedError


def work(request: HttpRequest, work_ident: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_wayback(request: HttpRequest, work_ident: str, url: str) -> HttpResponse:
    raise NotImplementedError


def access_redirect_ia_file(request: HttpRequest, work_ident: str, item: str, file_path: str) -> HttpResponse:
    raise NotImplementedError
