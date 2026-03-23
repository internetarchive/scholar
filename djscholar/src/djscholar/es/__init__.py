from django.conf import settings

from elasticsearch import Elasticsearch

_client = None


def client() -> Elasticsearch:
    global _client

    if _client is None:
        _client = Elasticsearch(
            settings.ES_HOSTS,
            sniff_on_start=settings.ES_SNIFF,
            sniff_on_connection_fail=settings.ES_SNIFF,
            sniffer_timeout=60,
        )
    return _client
