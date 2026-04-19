---
name: translation
description: Translate text between languages. Use when the user asks to translate content, detect language, or work with multilingual text.
metadata:
  fastclaw:
    always: false
---

# Translation Skill

Translate text between languages using the exec tool with Python.

## How to Translate

Use the `exec` tool to run a Python script that performs translation. Use the `deep-translator` library (pip install if needed).

```python
# Install if needed, then translate
import subprocess
subprocess.check_call(["pip", "install", "-q", "deep-translator"])

from deep_translator import GoogleTranslator

text = "YOUR_TEXT_HERE"
source = "auto"  # auto-detect source language
target = "en"    # target language code

result = GoogleTranslator(source=source, target=target).translate(text)
print(result)
```

## Language Codes
Common codes: en (English), zh-CN (Chinese Simplified), zh-TW (Chinese Traditional), ja (Japanese), ko (Korean), fr (French), de (German), es (Spanish), pt (Portuguese), ru (Russian), ar (Arabic), hi (Hindi)

## Guidelines
- Auto-detect source language when not specified
- Default target is English unless the user specifies otherwise
- For long texts, split into chunks if needed (max ~5000 chars per request)
- Always show both original and translated text
- If the user asks "what language is this", detect and report without translating
