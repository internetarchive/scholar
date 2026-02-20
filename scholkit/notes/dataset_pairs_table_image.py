#!/usr/bin/env -S uv run --script
# /// script
# dependencies = [
#   "dataframe-image",
#   "pandas",
# ]
# ///
import pandas as pd
import dataframe_image as dfi

if __name__ == "__main__":
    df = pd.read_csv(
        "2024-02-09-results-no-fatcat.tsv",
        sep="\t",
        names=["A", "B", "A and B", "only in A", "only in B"],
    )
    dfi.export(df, "df_styled.png")
