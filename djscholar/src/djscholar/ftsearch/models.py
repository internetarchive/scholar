from django.db import models


REFERRER_BUCKETS = [
    ("direct", "direct"),
    ("google_scholar", "google_scholar"),
    ("google", "google"),
    ("other", "other"),
]


class DailyAccessStat(models.Model):
    date = models.DateField()
    access_type = models.CharField(
        max_length=20,
        choices=[("wayback", "wayback"), ("ia_file", "ia_file")],
    )
    referrer_bucket = models.CharField(
        max_length=20,
        choices=REFERRER_BUCKETS,
        default="direct",
    )
    count = models.PositiveIntegerField(default=0)

    class Meta:
        constraints = [
            models.UniqueConstraint(
                fields=["date", "access_type", "referrer_bucket"],
                name="ftsearch_dailyaccessstat_date_type_ref_uniq",
            ),
        ]
        indexes = [
            models.Index(
                fields=["date"],
                name="ftsearch_dailyaccessstat_date_idx",
            ),
        ]
