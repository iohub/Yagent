#!/bin/bash
set -e
REPO="/tmp/repos/python_03_regex_engine"
rm -rf "$REPO"
mkdir -p "$REPO/regex_engine"
cd "$REPO"
touch regex_engine/__init__.py

cat > regex_engine/matcher.py << 'PYEOF'
# TODO: Implement NFA-based regex engine

def compile(pattern: str):
    """Compile a regex pattern string into an NFA."""
    pass

def match(pattern: str, text: str) -> bool:
    """Return True if pattern matches the entire text."""
    pass

def search(pattern: str, text: str) -> bool:
    """Return True if pattern matches anywhere in text."""
    pass
PYEOF

mkdir -p tests
cat > tests/test_regex.py << 'PYEOF'
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from regex_engine.matcher import match, search, compile

def test_literal_match():
    assert match("hello", "hello")
    assert not match("hello", "world")

def test_dot_wildcard():
    assert match("h.llo", "hello")
    assert match("h.llo", "hxllo")
    assert not match("h.llo", "hllo")

def test_star_quantifier():
    assert match("ab*c", "ac")
    assert match("ab*c", "abc")
    assert match("ab*c", "abbbbbbc")
    assert not match("ab*c", "adc")

def test_plus_quantifier():
    assert not match("ab+c", "ac")
    assert match("ab+c", "abc")
    assert match("ab+c", "abbbbc")

def test_optional():
    assert match("colou?r", "color")
    assert match("colou?r", "colour")
    assert not match("colou?r", "colouur")

def test_alternation():
    assert match("cat|dog", "cat")
    assert match("cat|dog", "dog")
    assert not match("cat|dog", "bird")

def test_character_class():
    assert match("[abc]", "a")
    assert match("[abc]", "b")
    assert match("[abc]", "c")
    assert not match("[abc]", "d")

def test_negated_character_class():
    assert match("[^abc]", "d")
    assert match("[^abc]", "x")
    assert not match("[^abc]", "a")

def test_range_in_class():
    assert match("[a-z]", "m")
    assert match("[0-9]", "5")
    assert not match("[a-z]", "5")

def test_start_anchor():
    assert match("^hello", "hello world")
    assert not search("^hello", "say hello")

def test_end_anchor():
    assert match("world$", "hello world")
    assert not search("world$", "world peace")

def test_group_capturing_semantics():
    assert search("(ab)+", "ababab")
    assert not search("(ab)+", "ababa")

def test_escaped_special_chars():
    assert match(r"\*hello\*", "*hello*")
    assert match(r"a\+b", "a+b")
    assert not match(r"a\+b", "ab")

def test_mixed_special():
    assert match("a.*b", "axyzb")
    assert match("a.+b", "axyzb")
    assert match("colou?r.*end", "color is the end")

def test_nested_character_classes():
    assert match("[a-c[x-z]]", "a")  # union
    assert match("[a-c[x-z]]", "y")

PYEOF

echo "python_03_regex_engine setup done"
