#!/usr/bin/env python3

"""
Helper script to convert an IA SIM KBART file to the format used for fatcat
exports to Keeper's Registry. This removes a couple fields, and adds ISSN-L via
lookup from a local TSV file.

Takes two arguments: IA SIM KBART file and ISSN-to-ISSNL mapping file.

You can find the SIM KBART files at
https://archive.org/download/internetarchive-rapid, and the ISSN/ISSN-L files
in the `ia_biblio_meta` collection, under recent chocula source items.

Columns used by IA KBART generation:

    publication_title
    print_identifier
    online_identifier
    date_first_issue_online
    num_first_vol_online
    num_first_issue_online
    date_last_issue_online
    num_last_vol_online
    num_last_issue_online
    title_url
    first_author
    title_id
    embargo_info
    coverage_depth
    coverage_notes
    publisher_name
    publication_type
    date_monograph_published_print
    date_monograph_published_online
    monograph_volume
    monograph_edition
    first_editor
    parent_publication_title_id
    preceding_publication_title_id
    access_type
    oclc_number
"""

import sys
import csv

KBART_FIELD_NAMES = [
    'publication_type',
    'publication_title',
    'print_identifier',
    'online_identifier',
    'date_first_issue_online',
    'num_first_vol_online',
    'num_first_issue_online',
    'date_last_issue_online',
    'num_last_vol_online',
    'num_last_issue_online',
    'title_url',
    'first_author',
    'title_id',
    'coverage_depth',
    'coverage_notes',
    'publisher_name',
    'linking_issn',
]

def run_convert(in_file, issn_issnl_file_path):
    print("Loading ISSN map file...", file=sys.stderr)
    ISSN_ISSNL_MAP = dict()
    with open(issn_issnl_file_path, 'r') as issn_issnl_file:
        for line in issn_issnl_file:
            if line.startswith("ISSN") or len(line) == 0:
                continue
            (issn, issnl) = line.split()[0:2]
            ISSN_ISSNL_MAP[issn] = issnl
            # double mapping makes lookups easy
            ISSN_ISSNL_MAP[issnl] = issnl
    print("Got {} ISSN-L mappings.".format(len(ISSN_ISSNL_MAP)), file=sys.stderr)

    kbart_reader = csv.DictReader(in_file, dialect='excel-tab')
    kbart_writer = csv.DictWriter(sys.stdout, fieldnames=KBART_FIELD_NAMES, dialect='excel-tab')
    kbart_writer.writeheader()


    for in_row in kbart_reader:
        #print(in_row)
        out_row = {}
        for k in KBART_FIELD_NAMES:
            out_row[k] = in_row.get(k, '')

        # lookup ISSN-L from ISSN-E or ISSN-P; skip if not found
        if not (out_row.get('online_identifier') or out_row.get('print_identifier')):
            continue
        if not out_row.get('linking_issn'):
            out_row['linking_issn'] = ISSN_ISSNL_MAP.get(out_row.get('online_identifier'), '')
        if not out_row.get('linking_issn'):
            out_row['linking_issn'] = ISSN_ISSNL_MAP.get(out_row.get('print_identifier'), '')
        if not out_row.get('linking_issn'):
            print(f"  no matching ISSN-L for: '{out_row.get('online_identifier')}' or '{out_row.get('print_identifier')}'. skipping", file=sys.stderr)
            continue

        # remove 'title_url' which points to archive.org (?)
        out_row['title_url'] = ''
        assert out_row['publication_title']
        assert out_row['coverage_depth'] == 'fulltext'

        kbart_writer.writerow(out_row)
        #print(out_row)

if __name__ == '__main__':
    if len(sys.argv) != 3:
        print("two args required: <sim-kbart-file> <issn-issnl-file>", file=sys.stderr)
        sys.exit(-1)
    with open(sys.argv[1], 'r') as in_file:
        run_convert(in_file, sys.argv[2])
