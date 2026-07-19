"""M7-3 协作式取消的 Python 侧单测。"""
import graph


def test_run_graph_cancelled_via_check(monkeypatch):
    """cancel_check 恒 True:离线 run 应立即以 CANCELLED 收尾,不执行图。"""
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    result = graph.run_graph("any task", params={"demo_delay_ms": "5000"},
                             cancel_check=lambda: True)
    assert result["status"] == "CANCELLED"
    assert result["output"] == "cancelled by user"


def test_run_graph_not_cancelled(monkeypatch):
    monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
    result = graph.run_graph("hello", cancel_check=lambda: False)
    assert result["status"] == "OK"


def test_interruptible_sleep_aborts_quickly():
    import time
    t0 = time.monotonic()
    try:
        graph._interruptible_sleep(10.0, cancel_check=lambda: True)
        raised = False
    except graph._RunCancelled:
        raised = True
    assert raised and time.monotonic() - t0 < 1.0
