---
name: web-search
description: Search the web and fetch web pages. Use when the user asks to search for information, look something up, or fetch a URL.
metadata:
  fastclaw:
    always: false
---

# Web Search Skill

Search the web and fetch pages using the available tools.

## Using web_fetch Tool
If the `web_fetch` tool is available, use it to fetch and read web pages:
```
web_fetch(url="https://example.com")
```

## Using exec with Python (fallback)
If web_fetch is not available, use Python with requests:

```python
import subprocess
subprocess.check_call(["pip", "install", "-q", "requests", "beautifulsoup4"])

import requests
from bs4 import BeautifulSoup

# Fetch a page
resp = requests.get("https://example.com", timeout=10)
soup = BeautifulSoup(resp.text, 'html.parser')

# Extract text content
text = soup.get_text(separator='\n', strip=True)
print(text[:2000])  # First 2000 chars
```

## Guidelines
- Prefer the `web_fetch` tool if available — it's faster and cleaner
- When using Python, always install packages first
- Limit output to relevant content — don't dump entire pages
- Summarize long content for the user
- Respect robots.txt and rate limits
