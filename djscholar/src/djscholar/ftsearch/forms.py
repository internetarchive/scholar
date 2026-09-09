from django import forms
from django.urls import reverse_lazy
from django_altcha import AltchaField


class SearchVerificationForm(forms.Form):
    """
    The one-field form behind the fulltext search gate.

    The field name has to stay "altcha": the widget renders an
    <altcha-widget name="altcha"> web component that posts the solved payload
    under that name.
    """

    altcha = AltchaField(
        label="",
        # Fetch the challenge from our own endpoint at solve time instead of
        # baking one into the page.  A verify page that gets cached (by a
        # browser, or by anything in front of us) would otherwise hand every
        # visitor the same challenge, and an expired one at that.
        challengeurl=reverse_lazy("ftsearch:altcha_challenge"),
    )
