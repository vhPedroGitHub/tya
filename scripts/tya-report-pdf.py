#!/usr/bin/env python3
"""
tya-report-pdf.py — Generate a PDF report from a TYA JSON report file.

Usage:
    python tya-report-pdf.py tya-report-20250101-120000.json
    python tya-report-pdf.py tya-report-20250101-120000.json --output my-report.pdf

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
# Colour palette
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


def _error_rate(total: int, failed: int) -> str:
    if total == 0:
        return "0.00%"
    return f"{failed / total * 100:.2f}%"


def _status_colour(failed: int, total: int) -> colors.Color:
    if total == 0:
        return MID_GRAY
    rate = failed / total
    if rate == 0:
        return GREEN
    if rate < 0.05:
        return ORANGE
    return RED


def _parse_ts(ts: str) -> str:
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        return dt.strftime("%Y-%m-%d %H:%M:%S UTC")
    except Exception:
        return ts


# ---------------------------------------------------------------------------
# Document builder
# ---------------------------------------------------------------------------

def build_pdf(report: dict, output_path: str) -> None:
    styles = getSampleStyleSheet()

    title_style = ParagraphStyle(
        "TYATitle",
        parent=styles["Title"],
        fontSize=26,
        textColor=DARK,
        spaceAfter=4,
    )
    subtitle_style = ParagraphStyle(
        "TYASubtitle",
        parent=styles["Normal"],
        fontSize=10,
        textColor=colors.HexColor("#7f8c8d"),
        spaceAfter=2,
    )
    h2_style = ParagraphStyle(
        "TYAH2",
        parent=styles["Heading2"],
        fontSize=14,
        textColor=ACCENT,
        spaceBefore=14,
        spaceAfter=4,
        borderPad=0,
    )
    h3_style = ParagraphStyle(
        "TYAH3",
        parent=styles["Heading3"],
        fontSize=11,
        textColor=DARK,
        spaceBefore=10,
        spaceAfter=3,
    )
    body_style = ParagraphStyle(
        "TYABody",
        parent=styles["Normal"],
        fontSize=9,
        textColor=DARK,
        leading=14,
    )
    small_style = ParagraphStyle(
        "TYASmall",
        parent=styles["Normal"],
        fontSize=8,
        textColor=colors.HexColor("#7f8c8d"),
    )

    doc = SimpleDocTemplate(
        output_path,
        pagesize=A4,
        leftMargin=2 * cm,
        rightMargin=2 * cm,
        topMargin=2 * cm,
        bottomMargin=2 * cm,
        title="TYA Run Report",
        author="TYA — Test Your API",
    )

    story = []

    # ------------------------------------------------------------------
    # Cover / header
    # ------------------------------------------------------------------
    story.append(Paragraph("TYA — Test Your API", title_style))
    story.append(Paragraph("Load Test Report", subtitle_style))
    story.append(HRFlowable(width="100%", thickness=2, color=ACCENT, spaceAfter=10))

    # Run metadata table
    run_id = report.get("run_id", "—")
    started = _parse_ts(report.get("started_at", ""))
    finished = _parse_ts(report.get("finished_at", ""))
    duration = f"{report.get('duration_s', 0):.2f} s"
    flows_count = len(report.get("flows", {}))

    meta_data = [
        ["Run ID", run_id],
        ["Started", started],
        ["Finished", finished],
        ["Total duration", duration],
        ["Flows executed", str(flows_count)],
    ]
    meta_table = Table(meta_data, colWidths=[4 * cm, 12 * cm])
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
    story.append(Spacer(1, 0.6 * cm))

    # ------------------------------------------------------------------
    # Summary table — one row per flow
    # ------------------------------------------------------------------
    story.append(Paragraph("Summary", h2_style))
    story.append(HRFlowable(width="100%", thickness=0.5, color=MID_GRAY, spaceAfter=6))

    flows: dict = report.get("flows", {})

    summary_header = [
        Paragraph("<b>Flow</b>", body_style),
        Paragraph("<b>Type</b>", body_style),
        Paragraph("<b>Total req</b>", body_style),
        Paragraph("<b>Success</b>", body_style),
        Paragraph("<b>Failed</b>", body_style),
        Paragraph("<b>Error rate</b>", body_style),
        Paragraph("<b>RPS</b>", body_style),
        Paragraph("<b>p50 (ms)</b>", body_style),
        Paragraph("<b>p95 (ms)</b>", body_style),
        Paragraph("<b>p99 (ms)</b>", body_style),
    ]
    summary_rows = [summary_header]

    for flow_name, flow in flows.items():
        total = flow.get("total_requests", 0)
        failed = flow.get("failed_requests", 0)
        lat = flow.get("latency_ms", {})
        rate = failed / total if total > 0 else 0
        cell_color = _status_colour(failed, total)
        err_cell = Paragraph(
            f'<font color="{cell_color.hexval()}">{_error_rate(total, failed)}</font>',
            body_style,
        )
        summary_rows.append([
            Paragraph(flow_name, body_style),
            Paragraph(flow.get("type", ""), body_style),
            Paragraph(str(total), body_style),
            Paragraph(str(flow.get("successful_requests", 0)), body_style),
            Paragraph(str(failed), body_style),
            err_cell,
            Paragraph(_fmt_float(flow.get("rps_achieved", 0), 1), body_style),
            Paragraph(_fmt_ms(lat.get("p50", 0)), body_style),
            Paragraph(_fmt_ms(lat.get("p95", 0)), body_style),
            Paragraph(_fmt_ms(lat.get("p99", 0)), body_style),
        ])

    col_w = [3.8 * cm, 1.8 * cm, 1.6 * cm, 1.6 * cm, 1.4 * cm,
             1.8 * cm, 1.4 * cm, 1.8 * cm, 1.8 * cm, 1.8 * cm]
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
        ("ALIGN", (2, 0), (-1, -1), "RIGHT"),
    ]))
    story.append(summary_table)

    # ------------------------------------------------------------------
    # Per-flow detail pages
    # ------------------------------------------------------------------
    for flow_name, flow in flows.items():
        story.append(PageBreak())
        story.append(Paragraph(f"Flow: {flow_name}", h2_style))
        story.append(HRFlowable(width="100%", thickness=0.5, color=MID_GRAY, spaceAfter=6))

        total = flow.get("total_requests", 0)
        failed = flow.get("failed_requests", 0)
        lat = flow.get("latency_ms", {})

        # Flow KPIs
        kpi_data = [
            ["Type", flow.get("type", "—"),
             "RPS achieved", _fmt_float(flow.get("rps_achieved", 0), 1)],
            ["Total requests", str(total),
             "Error rate", _error_rate(total, failed)],
            ["Successful", str(flow.get("successful_requests", 0)),
             "Failed", str(failed)],
        ]
        kpi_table = Table(kpi_data, colWidths=[3 * cm, 4 * cm, 3 * cm, 4 * cm])
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
        story.append(Spacer(1, 0.4 * cm))

        # Latency breakdown
        story.append(Paragraph("Latency (ms)", h3_style))
        lat_data = [
            [Paragraph("<b>Min</b>", body_style),
             Paragraph("<b>Mean</b>", body_style),
             Paragraph("<b>p50</b>", body_style),
             Paragraph("<b>p90</b>", body_style),
             Paragraph("<b>p95</b>", body_style),
             Paragraph("<b>p99</b>", body_style),
             Paragraph("<b>Max</b>", body_style)],
            [_fmt_ms(lat.get("min", 0)),
             _fmt_ms(lat.get("mean", 0)),
             _fmt_ms(lat.get("p50", 0)),
             _fmt_ms(lat.get("p90", 0)),
             _fmt_ms(lat.get("p95", 0)),
             _fmt_ms(lat.get("p99", 0)),
             _fmt_ms(lat.get("max", 0))],
        ]
        lat_table = Table(lat_data, colWidths=[2.4 * cm] * 7)
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
        story.append(Spacer(1, 0.4 * cm))

        # Steps table
        steps = flow.get("steps", [])
        if steps:
            story.append(Paragraph("Steps", h3_style))
            _render_steps_table(story, steps, body_style, col_multiplier=1.0)

        # Children table
        children = flow.get("children", [])
        if children:
            story.append(Spacer(1, 0.3 * cm))
            story.append(Paragraph("Wire-flow children", h3_style))
            story.append(Paragraph(
                "These steps ran after the load pool drained (teardown / verification).",
                small_style,
            ))
            story.append(Spacer(1, 0.2 * cm))
            _render_steps_table(story, children, body_style, col_multiplier=1.0)

        # Errors by status
        errors_by_status: dict = flow.get("errors_by_status", {})
        if errors_by_status:
            story.append(Spacer(1, 0.3 * cm))
            story.append(Paragraph("Errors by HTTP status", h3_style))
            ebs_rows = [[Paragraph("<b>Status</b>", body_style),
                         Paragraph("<b>Count</b>", body_style)]]
            for code, count in sorted(errors_by_status.items()):
                ebs_rows.append([code, str(count)])
            ebs_table = Table(ebs_rows, colWidths=[4 * cm, 4 * cm])
            ebs_table.setStyle(TableStyle([
                ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
                ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
                ("FONTSIZE", (0, 0), (-1, -1), 9),
                ("ROWBACKGROUNDS", (0, 1), (-1, -1), [LIGHT_GRAY, WHITE]),
                ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
                ("TOPPADDING", (0, 0), (-1, -1), 4),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
                ("LEFTPADDING", (0, 0), (-1, -1), 6),
            ]))
            story.append(ebs_table)

    # ------------------------------------------------------------------
    # Build
    # ------------------------------------------------------------------
    doc.build(story)


def _render_steps_table(story, steps: list, body_style, col_multiplier: float = 1.0):
    header = [
        Paragraph("<b>Step ID</b>", body_style),
        Paragraph("<b>Requests</b>", body_style),
        Paragraph("<b>Errors</b>", body_style),
        Paragraph("<b>Error %</b>", body_style),
        Paragraph("<b>p50 (ms)</b>", body_style),
        Paragraph("<b>p95 (ms)</b>", body_style),
        Paragraph("<b>p99 (ms)</b>", body_style),
        Paragraph("<b>Mean (ms)</b>", body_style),
    ]
    rows = [header]
    for step in steps:
        total = step.get("requests", 0)
        failed = step.get("errors", 0)
        lat = step.get("latency_ms", {})
        rows.append([
            Paragraph(step.get("step_id", "—"), body_style),
            str(total),
            str(failed),
            _error_rate(total, failed),
            _fmt_ms(lat.get("p50", 0)),
            _fmt_ms(lat.get("p95", 0)),
            _fmt_ms(lat.get("p99", 0)),
            _fmt_ms(lat.get("mean", 0)),
        ])
    col_w = [4 * cm, 1.8 * cm, 1.5 * cm, 1.8 * cm,
             2.0 * cm, 2.0 * cm, 2.0 * cm, 2.0 * cm]
    tbl = Table(rows, colWidths=col_w, repeatRows=1)
    tbl.setStyle(TableStyle([
        ("BACKGROUND", (0, 0), (-1, 0), ACCENT),
        ("TEXTCOLOR", (0, 0), (-1, 0), WHITE),
        ("FONTSIZE", (0, 0), (-1, -1), 8),
        ("ROWBACKGROUNDS", (0, 1), (-1, -1), [colors.HexColor("#f4f6f8"), WHITE]),
        ("GRID", (0, 0), (-1, -1), 0.3, MID_GRAY),
        ("TOPPADDING", (0, 0), (-1, -1), 4),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 4),
        ("LEFTPADDING", (0, 0), (-1, -1), 4),
        ("ALIGN", (1, 0), (-1, -1), "RIGHT"),
    ]))
    story.append(tbl)


# ---------------------------------------------------------------------------
# CLI entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Generate a PDF report from a TYA JSON report file.",
    )
    parser.add_argument("report", help="Path to tya-report-*.json")
    parser.add_argument(
        "--output", "-o",
        help="Output PDF path (default: same name as input with .pdf extension)",
    )
    args = parser.parse_args()

    input_path = Path(args.report)
    if not input_path.exists():
        print(f"ERROR: file not found: {input_path}")
        sys.exit(1)

    output_path = args.output or input_path.with_suffix(".pdf")

    with open(input_path) as f:
        try:
            report = json.load(f)
        except json.JSONDecodeError as e:
            print(f"ERROR: invalid JSON: {e}")
            sys.exit(1)

    print(f"Generating PDF from {input_path} ...")
    build_pdf(report, str(output_path))
    print(f"Report written to {output_path}")


if __name__ == "__main__":
    main()
