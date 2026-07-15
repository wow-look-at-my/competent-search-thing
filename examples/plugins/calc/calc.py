#!/usr/bin/env python3
"""Calculator plugin for competent-search-thing.

Reads ONE JSON request from stdin, evaluates the stripped query as
plain arithmetic (through an ast whitelist -- never eval), and writes
one JSON response to stdout. Anything that is not simple arithmetic
yields an empty result list; the script never exits non-zero.
"""

import ast
import json
import math
import operator
import sys

# Bounds so absurd expressions ("9**9**9") cannot hang the process or
# produce numbers too large to display.
MAX_EXPONENT = 256
MAX_POW_BASE = 1 << 256
MAX_TITLE_CHARS = 200  # the searchbar truncates titles at 200 runes

_BINOPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.FloorDiv: operator.floordiv,
    ast.Mod: operator.mod,
}


def _power(base, exponent):
    """Bounded ** so huge exponents are refused instead of computed."""
    if isinstance(base, int) and isinstance(exponent, int):
        if abs(exponent) > MAX_EXPONENT or abs(base) > MAX_POW_BASE:
            raise ValueError("exponent out of range")
    return base ** exponent


def _evaluate(node):
    """Evaluate a whitelisted arithmetic AST node."""
    if isinstance(node, ast.Expression):
        return _evaluate(node.body)
    if isinstance(node, ast.Constant):
        # type() rather than isinstance(): bool subclasses int.
        if type(node.value) in (int, float):
            return node.value
        raise ValueError("non-numeric constant")
    if isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.UAdd, ast.USub)):
        value = _evaluate(node.operand)
        return -value if isinstance(node.op, ast.USub) else value
    if isinstance(node, ast.BinOp):
        if isinstance(node.op, ast.Pow):
            return _power(_evaluate(node.left), _evaluate(node.right))
        fn = _BINOPS.get(type(node.op))
        if fn is not None:
            return fn(_evaluate(node.left), _evaluate(node.right))
    raise ValueError("unsupported expression")


def _format(value):
    """Format a number plainly: ints verbatim, floats trimmed via %g
    so float artifacts collapse (0.1+0.2 -> 0.3, 1/4 -> 0.25)."""
    if isinstance(value, int):
        text = str(value)
        if len(text) > MAX_TITLE_CHARS:
            raise ValueError("result too long to display")
        return text
    if not math.isfinite(value):
        raise ValueError("not a finite number")
    return "%.12g" % value


def _result(stripped):
    value = _evaluate(ast.parse(stripped, mode="eval"))
    title = _format(value)
    result = {
        "title": title,
        "subtitle": stripped + " =",
        "icon": "calculator",
        "badge": "CALC",
        "accent_color": "#a6e3a1",
        "score": 100,
        "action": {"type": "copy_text", "value": title},
    }
    # Hex/Binary make sense for integers only. Skip values whose binary
    # form would exceed the 200-rune field cap and render truncated.
    if isinstance(value, int) and value.bit_length() <= 198:
        result["fields"] = [
            {"label": "Hex", "value": hex(value)},
            {"label": "Binary", "value": bin(value)},
        ]
    return result


def main():
    request = json.load(sys.stdin)
    stripped = str(request.get("stripped") or "").strip()
    if not stripped:
        return []
    return [_result(stripped)]


if __name__ == "__main__":
    try:
        results = main()
    except Exception:  # any problem means "no results", never a crash
        results = []
    json.dump({"v": 1, "results": results}, sys.stdout)
