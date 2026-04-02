from django.db import models


class DailyAccessStat(models.Model):
    date = models.DateField()
    access_type = models.CharField(
        max_length=20,
        choices=[("wayback", "wayback"), ("ia_file", "ia_file")],
    )
    count = models.PositiveIntegerField(default=0)

    class Meta:
        constraints = [
            models.UniqueConstraint(
                fields=["date", "access_type"],
                name="ftsearch_dailyaccessstat_date_type_uniq",
            ),
        ]
        indexes = [
            models.Index(
                fields=["date"],
                name="ftsearch_dailyaccessstat_date_idx",
            ),
        ]
