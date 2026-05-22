#!/usr/bin/env python3
"""
tya-k6-report-pdf.py — Generate a PDF report from k6 JSON summary files.

Reads k6 --summary-export JSON files (one per flow) from a reports/ directory
and produces a consolidated PDF with per-flow metrics, latency percentiles,
error breakdowns, and throughput analysis.

Usage:
    python tya-k6-report-pdf.py reports/
    python tya-k6-report-pdf.py reports/ --output k6-report.pdf
    python tya-k6-report-pdf.py reports/seed-users-summary.json

Requirements:
    pip install reportlab
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path

try:
    from reportlab.lib import colors
    from reportlab.lib.pagesizes import A4
    from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
    from reportlab.lib.units import cm
    from reportlab.platypus import (
        HRFlowable,
        PageBreak,
        Paragraph,
        SimpleDocTemplate,
        Spacer,
        Table,
        TableStyle,
    )
except ImportError:
    print("ERROR: reportlab is required. Install it with:  pip install reportlab")
    sys.exit(1)


# ---------------------------------------------------------------------------
# Colour palette (matches tya-report-pdf.py)
# ---------------------------------------------------------------------------
DARK = colors.HexColor("#1a1a2e")
ACCENT = colors.HexColor("#0f3460")
GREEN = colors.HexColor("#16a085")
RED = colors.HexColor("#e74c3c")
ORANGE = colors.HexColor("#e67e22")
LIGHT_GRAY = colors.HexColor("#f4f6f8")
MID_GRAY = colors.HexColor("#bdc3c7")
WHITE = colors.white


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _fmt_ms(value: float) -> str:
    return f"{value:.1f} ms"


def _fmt_float(value: float, decimals: int = 2) -> str:
    return f"{value:.{decimals}f}"


def _fmt_bytes(value: float) -> str:
    if value >= 1_048_576:
        return f"{value / 1_048_576:.1f} MB"
    if value >= 1024:
        return f"{value / 1024:.1f} KB"
    return f"{value:.0f} B"


def _error_rate(passes: int, fails: int) -> str:
    total = passes + fails
    if total == 0:
        return "0.00%"
    return f"{fails / total * 100:.2f}%"


def _status_colour(fails: int, passes: int) -> colors.Color:
    total = passes + fails
    if total == 0:
        return MID_GRAY
    rate = fails / total
    if rate == 0:
        return GREEN
    if rate < 0.05:
        return ORANGE
    return RED


def _get_metric(data: dict, name: str, field: str, default=0):
    """Safely extract a field from a k6 metric."""
    metric = data.get("metrics", {}).get(name, {})
    return metric.get(field, default)


# ---------------------------------------------------------------------------
# k6 JSON report loading
# ---------------------------------------------------------------------------

def load_k6_reports(reports_dir: Path) -> list:
    """Load all *-summary.json files from a directory."""
    reports = []
    if reports_dir.is_file():
        # Single file mode
        with open(reports_dir) as f:
            data = json.load(f)
        reports.append((reports_dir.stem.replace("-summary", ""), data))
        return reports

    for f in sorted(reports_dir.glob("*-summary.json")):
        with open(f) as fh:
            try:
                data = json.load(fh)
                name = f.stem.replace("-summary", "")
                reports.append((name, data))
            except json.JSONDecodeError:
                print(f"WARNING: skipping invalid JSON: {f}")
    return reports


# ---------------------------------------------------------------------------
# PDF builder
# ---------------------------------------------------------------------------

def build_pdf(reports: list, output_path: str, config: dict = None) -> None:
    styles = getSampleStyleSheet()

    title_style = ParagraphStyle(
        "TYATitle", parent=styles["Title"], fontSize=26,
        textColor=DARK, spaceAfter=4,
    )
    subtitle_style = ParagraphStyle(
        "TYASubtitle", parent=styles["Normal"], fontSize=10,
        textColor=colors.HexColor("#7f8c8d"), spaceAfter=2,
    )
    h2_style = ParagraphStyle(
        "TYAH2", parent=styles["Heading2"], fontSize=14,
        textColor=ACCENT, spaceBefore=14, spaceAfter=4,
    )
    h3_style = ParagraphStyle(
        "TYAH3", parent=styles["Heading3"], fontSize=11,
        textColor=DARK, spaceBefore=10, spaceAfter=3,
    )
    body_style = ParagraphStyle(
        "TYABody", parent=styles["Normal"], fontSize=9,
        textColor=DARK, leading=14,
    )
    small_style = ParagraphStyle(
        "TYASmall", parent=styles["Normal"], fontSize=8,
        textColor=colors.HexColor("#7f8c8d"),
    )

    doc = SimpleDocTemplate(
        output_path, pagesize=A4,
        leftMargin=2*cm, rightMargin=2*cm,
        topMargin=2*cm, bottomMargin=2*cm,
        title="TYA k6 Load Test Report",
        author="TYA — Test Your API",
    )

    story = []

    # Header
    story.append(Paragraph("TYA — k6 Load Test Report", title_style))
    story.append(Paragraph("Generated by TYA from k6 JSON summary", subtitle_style))
    story.append(HRFlowable(width="100%", thickness=2, color=ACCENT, spaceAfter=10))

    # Run metadata
    base_url = config.get("base_url", "—") if config else "—"
    flows_count = len(reports)
    meta_data = [
        ["Base URL", base_url],
        ["Flows executed", str(flows_count)],
        ["Generated", datetime.now().strftime("%Y-%m-%d %H:%M:%S")],
    ]
    meta_table = Table(meta_data, colWidths=[4*cm, 12*cm])
    meta_table.setStyle(TableStyle([
        ("FONTNAME", (0, 0), (-1, -1), "Helvetica"),
        ("FONTSIZE", (0, 0), (-1, -1), 9),
        ("FONTNAME", (0, 0), (0, -1), "Helvetica-Bold"),
        ("TEXTCOLOR", (0, 0), (0, -1), ACCENT),
        ("TEXTCOLOR", (1, 0), (1, -1), DARK),
        ("ROWBACKGROUNDS", (0, 0), (-1, -1), [LIGHT_GRAY, WHITE]),
        ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
        ("TOPPADDING", (0, 0), (-1, -1), 4),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
        ("LEFTPADDING", (0, 0), (-1, -1), 6),
    ]))
    story.append(meta_table)
    story.append(Spacer(1, 0.6*cm))

    # Summary table
    story.append(Paragraph("Summary", h2_style))
    story.append(HRFlowable(width="100%", thickness=0.5, color=MID_GRAY, spaceAfter=6))

    summary_header = [
        Paragraph("<b>Flow</b>", body_style),
        Paragraph("<b>Requests</b>", body_style),
        Paragraph("<b>RPS</b>", body_style),
        Paragraph("<b>Errors</b>", body_style),
        Paragraph("<b>Error %</b>", body_style),
        Paragraph("<b>Data sent</b>", body_style),
        Paragraph("<b>Data recv</b>", body_style),
        Paragraph("<b>p50 (ms)</b>", body_style),
        Paragraph("<b>p95 (ms)</b>", body_style),
        Paragraph("<b>p99 (ms)</b>", body_style),
    ]
    summary_rows = [summary_header]

    for name, data in reports:
        req_count = _get_metric(data, "http_reqs", "count", 0)
        req_rate = _get_metric(data, "http_reqs", "rate", 0)
        failed_passes = _get_metric(data, "http_req_failed", "passes", 0)
        failed_fails = _get_metric(data, "http_req_failed", "fails", 0)
        data_sent = _get_metric(data, "data_sent", "count", 0)
        data_recv = _get_metric(data, "data_received", "count", 0)
        p50 = _get_metric(data, "http_req_duration", "med", 0)
        p95 = _get_metric(data, "http_req_duration", "p(95)", 0)
        p99 = _get_metric(data, "http_req_duration", "p(99)", 0)

        cell_color = _status_colour(int(failed_fails), int(failed_passes))
        err_cell = Paragraph(
            f'<font color="{cell_color.hexval()}">{_error_rate(int(failed_passes), int(failed_fails))}</font>',
            body_style,
        )

        summary_rows.append([
            Paragraph(name, body_style),
            Paragraph(str(int(req_count)), body_style),
            Paragraph(_fmt_float(req_rate, 1), body_style),
            Paragraph(str(int(failed_fails)), body_style),
            err_cell,
            Paragraph(_fmt_bytes(data_sent), body_style),
            Paragraph(_fmt_bytes(data_recv), body_style),
            Paragraph(_fmt_ms(p50), body_style),
            Paragraph(_fmt_ms(p95), body_style),
            Paragraph(_fmt_ms(p99), body_style),
        ])

    col_w = [3*cm, 1.6*cm, 1.4*cm, 1.4*cm, 1.6*cm,
             1.6*cm, 1.6*cm, 1.8*cm, 1.8*cm, 1.8*cm]
    summary_table = Table(summary_rows, colWidths=col_w, repeatRows=1)
    summary_table.setStyle(TableStyle([
        ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
        ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
        ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
        ("FONTSIZE", (0, 0), (-1, -1), 8),
        ("ROWBACKGROUNDS", (0, 1), (-1, -1), [LIGHT_GRAY, WHITE]),
        ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
        ("TOPPADDING", (0, 0), (-1, -1), 4),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
        ("LEFTPADDING", (0, 0), (-1, -1), 4),
        ("ALIGN", (1, 0), (-1, -1), "RIGHT"),
    ]))
    story.append(summary_table)

    # Per-flow detail pages
    for name, data in reports:
        story.append(PageBreak())
        story.append(Paragraph(f"Flow: {name}", h2_style))
        story.append(HRFlowable(width="100%", thickness=0.5, color=MID_GRAY, spaceAfter=6))

        req_count = _get_metric(data, "http_reqs", "count", 0)
        req_rate = _get_metric(data, "http_reqs", "rate", 0)
        failed_passes = _get_metric(data, "http_req_failed", "passes", 0)
        failed_fails = _get_metric(data, "http_req_failed", "fails", 0)
        data_sent = _get_metric(data, "data_sent", "count", 0)
        data_recv = _get_metric(data, "data_received", "count", 0)
        iterations = _get_metric(data, "iterations", "count", 0)
        iter_rate = _get_metric(data, "iterations", "rate", 0)
        vus_max = _get_metric(data, "vus_max", "value", 0)

        # KPI table
        kpi_data = [
            ["Total requests", str(int(req_count)),
             "Request rate", f"{req_rate:.1f} req/s"],
            ["Failed", str(int(failed_fails)),
             "Error rate", _error_rate(int(failed_passes), int(failed_fails))],
            ["Iterations", str(int(iterations)),
             "Iteration rate", f"{iter_rate:.2f} iter/s"],
            ["Max VUs", str(int(vus_max)),
             "Data sent", _fmt_bytes(data_sent)],
            ["Data received", _fmt_bytes(data_recv), "", ""],
        ]
        kpi_table = Table(kpi_data, colWidths=[3*cm, 4*cm, 3*cm, 4*cm])
        kpi_table.setStyle(TableStyle([
            ("FONTNAME", (0, 0), (-1, -1), "Helvetica"),
            ("FONTNAME", (0, 0), (0, -1), "Helvetica-Bold"),
            ("FONTNAME", (2, 0), (2, -1), "Helvetica-Bold"),
            ("FONTSIZE", (0, 0), (-1, -1), 9),
            ("TEXTCOLOR", (0, 0), (0, -1), ACCENT),
            ("TEXTCOLOR", (2, 0), (2, -1), ACCENT),
            ("ROWBACKGROUNDS", (0, 0), (-1, -1), [LIGHT_GRAY, WHITE]),
            ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
            ("TOPPADDING", (0, 0), (-1, -1), 4),
            ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
            ("LEFTPADDING", (0, 0), (-1, -1), 6),
        ]))
        story.append(kpi_table)
        story.append(Spacer(1, 0.4*cm))

        # Latency table
        story.append(Paragraph("HTTP Request Duration (ms)", h3_style))
        lat_min = _get_metric(data, "http_req_duration", "min", 0)
        lat_max = _get_metric(data, "http_req_duration", "max", 0)
        lat_avg = _get_metric(data, "http_req_duration", "avg", 0)
        lat_med = _get_metric(data, "http_req_duration", "med", 0)
        lat_p90 = _get_metric(data, "http_req_duration", "p(90)", 0)
        lat_p95 = _get_metric(data, "http_req_duration", "p(95)", 0)
        lat_p99 = _get_metric(data, "http_req_duration", "p(99)", 0)

        lat_data = [
            [Paragraph("<b>Min</b>", body_style),
             Paragraph("<b>Mean</b>", body_style),
             Paragraph("<b>Med (p50)</b>", body_style),
             Paragraph("<b>p90</b>", body_style),
             Paragraph("<b>p95</b>", body_style),
             Paragraph("<b>p99</b>", body_style),
             Paragraph("<b>Max</b>", body_style)],
            [_fmt_ms(lat_min), _fmt_ms(lat_avg), _fmt_ms(lat_med),
             _fmt_ms(lat_p90), _fmt_ms(lat_p95), _fmt_ms(lat_p99),
             _fmt_ms(lat_max)],
        ]
        lat_table = Table(lat_data, colWidths=[2.4*cm]*7)
        lat_table.setStyle(TableStyle([
            ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
            ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
            ("FONTSIZE", (0, 0), (-1, -1), 9),
            ("ALIGN", (0, 0), (-1, -1), "CENTER"),
            ("BACKGROUND", (0, 1), (-1, 1), LIGHT_GRAY),
            ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
            ("TOPPADDING", (0, 0), (-1, -1), 5),
            ("BOTTOMPADDING", (0, 0), (-1, -1), 5),
        ]))
        story.append(lat_table)
        story.append(Spacer(1, 0.4*cm))

        # Checks table
        checks = data.get("metrics", {}).get("checks", {})
        if checks:
            story.append(Paragraph("Checks", h3_style))
            check_passes = checks.get("passes", 0)
            check_fails = checks.get("fails", 0)
            check_data = [
                [Paragraph("<b>Passed</b>", body_style),
                 Paragraph("<b>Failed</b>", body_style),
                 Paragraph("<b>Rate</b>", body_style)],
                [str(int(check_passes)), str(int(check_fails)),
                 _error_rate(int(check_passes), int(check_fails))],
            ]
            check_table = Table(check_data, colWidths=[4*cm, 4*cm, 4*cm])
            check_table.setStyle(TableStyle([
                ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
                ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
                ("FONTSIZE", (0, 0), (-1, -1), 9),
                ("ALIGN", (0, 0), (-1, -1), "CENTER"),
                ("BACKGROUND", (0, 1), (-1, 1), LIGHT_GRAY),
                ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
                ("TOPPADDING", (0, 0), (-1, -1), 5),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 5),
            ]))
            story.append(check_table)
            story.append(Spacer(1, 0.4*cm))

        # Custom metrics (tya_errors, tya_step_latency)
        tya_errors = data.get("metrics", {}).get("tya_errors", {})
        tya_latency = data.get("metrics", {}).get("tya_step_latency", {})
        if tya_errors or tya_latency:
            story.append(Paragraph("TYA Custom Metrics", h3_style))
            custom_data = [
                [Paragraph("<b>Metric</b>", body_style),
                 Paragraph("<b>Count</b>", body_style),
                 Paragraph("<b>Rate</b>", body_style)],
            ]
            if tya_errors:
                custom_data.append([
                    Paragraph("tya_errors", body_style),
                    Paragraph(str(int(tya_errors.get("count", 0))), body_style),
                    Paragraph(f"{tya_errors.get('rate', 0):.2f}/s", body_style),
                ])
            if tya_latency:
                custom_data.append([
                    Paragraph("tya_step_latency (avg)", body_style),
                    Paragraph(_fmt_ms(tya_latency.get("avg", 0)), body_style),
                    Paragraph("", body_style),
                ])

            custom_table = Table(custom_data, colWidths=[5*cm, 4*cm, 4*cm])
            custom_table.setStyle(TableStyle([
                ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
                ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
                ("FONTSIZE", (0, 0), (-1, -1), 9),
                ("ROWBACKGROUNDS", (0, 1), (-1, -1), [LIGHT_GRAY, WHITE]),
                ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
                ("TOPPADDING", (0, 0), (-1, -1), 5),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 5),
            ]))
            story.append(custom_table)

    doc.build(story)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Generate a PDF report from k6 JSON summary files.",
    )
    parser.add_argument(
        "input",
        help="Path to a reports/ directory or a single *-summary.json file",
    )
    parser.add_argument(
        "--output", "-o",
        help="Output PDF path (default: k6-report.pdf in the input directory)",
    )
    parser.add_argument(
        "--config", "-c",
        help="Path to config.json (from tya genk6) for metadata",
    )
    args = parser.parse_args()

    input_path = Path(args.input)
    if not input_path.exists():
        print(f"ERROR: path not found: {input_path}")
        sys.exit(1)

    reports = load_k6_reports(input_path)
    if not reports:
        print("ERROR: no k6 summary JSON files found")
        sys.exit(1)

    # Load config if available
    config = None
    config_path = args.config
    if not config_path and input_path.is_dir():
        config_path = str(input_path / "config.json")
    if config_path:
        cp = Path(config_path)
        if cp.exists():
            with open(cp) as f:
                config = json.load(f)

    output_path = args.output
    if not output_path:
        if input_path.is_dir():
            output_path = str(input_path / "k6-report.pdf")
        else:
            output_path = str(input_path.with_suffix(".pdf"))

    print(f"Generating PDF from {len(reports)} k6 report(s)...")
    build_pdf(reports, output_path, config)
    print(f"Report written to {output_path}")


if __name__ == "__main__":
    main()
