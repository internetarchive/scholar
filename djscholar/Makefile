.PHONY: test serve check

test:
	DJANGO_SETTINGS_MODULE=djscholar.site.settings.base uv run pytest

serve:
	DJANGO_SETTINGS_MODULE=djscholar.site.settings.base uv run python manage.py runserver

check:
	uv run ruff check
