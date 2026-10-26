.PHONY: test test-podman test-db test-db-stop serve check

TEST_PG_CONTAINER := djscholar-test-postgres
TEST_PG_PORT ?= 5433

# assumes locally running pg
test:
	DJANGO_SETTINGS_MODULE=djscholar.site.settings.base uv run pytest

# use throwaway podman pg. NB: the container stays running unless manually stopped with test-db-stop target. this is to make repeated test runs faster.
test-podman: test-db
	DJANGO_SETTINGS_MODULE=djscholar.site.settings.ci \
	DJANGO_POSTGRESQL_HOST=127.0.0.1 \
	DJANGO_POSTGRESQL_PORT=$(TEST_PG_PORT) \
	uv run pytest $(PYTEST_ARGS)

test-db:
	podman start $(TEST_PG_CONTAINER) >/dev/null 2>&1 || \
	podman run -d --name $(TEST_PG_CONTAINER) \
		-e POSTGRES_USER=fatcat -e POSTGRES_PASSWORD=fatcat \
		-p 127.0.0.1:$(TEST_PG_PORT):5432 \
		--tmpfs /var/lib/postgresql/data \
		docker.io/library/postgres:17-bookworm >/dev/null
	@until podman exec $(TEST_PG_CONTAINER) pg_isready -q -U fatcat -h 127.0.0.1; do sleep 0.5; done

test-db-stop:
	podman rm -f $(TEST_PG_CONTAINER)

serve:
	DJANGO_SETTINGS_MODULE=djscholar.site.settings.base uv run python manage.py runserver

check:
	uv run ruff check
