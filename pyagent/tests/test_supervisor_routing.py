"""
Offline keyword-routing tests for graph._keyword_route.

Priority documented in graph.py: audit > review > coding > research.
No network / no DEEPSEEK_API_KEY required.
"""
import graph


def test_route_review():
    assert graph._keyword_route("please review this code for bugs") == "review"


def test_route_coding():
    assert graph._keyword_route("write a python function to sort a list") == "coding"


def test_route_research():
    assert graph._keyword_route("what is the capital of France") == "research"


def test_route_audit_pragma():
    assert graph._keyword_route("pragma solidity ^0.8.0\ncontract Foo {}") == "audit"


def test_route_audit_chinese():
    assert graph._keyword_route("重入漏洞审计") == "audit"  # 重入漏洞审计


def test_priority_review_over_coding():
    # Contains both a review keyword ("review") and a coding keyword ("code"/"function"):
    # review should win per documented priority (review checked before coding).
    task = "review this code and check the function for bugs"
    assert graph._keyword_route(task) == "review"


def test_priority_audit_over_review():
    # Contains both an audit-strong keyword ("pragma solidity") and a review keyword ("review"):
    # audit has top priority.
    task = "pragma solidity ^0.8.0 please review this contract"
    assert graph._keyword_route(task) == "audit"
