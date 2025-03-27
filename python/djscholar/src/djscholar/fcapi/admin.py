from django.contrib import admin

from .models import AuthToken


class AuthTokenAdmin(admin.ModelAdmin):
    pass


admin.site.register(AuthToken, AuthTokenAdmin)
