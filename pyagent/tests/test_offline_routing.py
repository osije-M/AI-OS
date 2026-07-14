"""
M6-C① 离线回归门禁的"秒级快挂"层。

读取 eval/suite-offline.yaml，逐条断言 graph._keyword_route(task) == expect_route。
expect_route 为空的用例（policy deny，Go 侧确定性规则，不经过 pyagent 路由）跳过。

这层比 evalctl 端到端跑全栈快得多：关键词表或基准集改坏了，pytest 秒级就红，
不用等 docker compose 起全栈。
"""
from pathlib import Path

import pytest
import yaml

import graph

_SUITE_PATH = Path(__file__).resolve().parents[2] / "eval" / "suite-offline.yaml"


def _load_cases():
    data = yaml.safe_load(_SUITE_PATH.read_text(encoding="utf-8"))
    return data["tasks"]


_CASES = _load_cases()

# 只对 expect_route 非空的用例做参数化断言；deny 用例（expect_route == ""）跳过，
# 因为 policy 拒绝发生在 Go 侧，不经过 pyagent 的 _keyword_route。
_ROUTED_CASES = [c for c in _CASES if c.get("expect_route")]


@pytest.mark.parametrize(
    "case",
    _ROUTED_CASES,
    ids=[c["id"] for c in _ROUTED_CASES],
)
def test_offline_suite_keyword_route(case):
    assert graph._keyword_route(case["task"]) == case["expect_route"]


def test_offline_suite_covers_expected_case_count():
    # 防止有人往 suite-offline.yaml 悄悄加/删用例却不更新预期规模。
    assert len(_CASES) == 12
    assert len(_ROUTED_CASES) == 9  # 12 条 - 3 条 deny
