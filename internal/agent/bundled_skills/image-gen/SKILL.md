---
name: image-gen
description: Generate images, charts, plots, and visualizations. Use when the user asks to draw, plot, chart, visualize data, or create images.
metadata:
  fastclaw:
    always: false
---

# Image Generation Skill

Generate images in the headless sandbox using Python libraries.

## Available Libraries

### Matplotlib (charts, plots, graphs)
```python
import matplotlib
matplotlib.use('Agg')  # REQUIRED: headless backend
import matplotlib.pyplot as plt

plt.figure(figsize=(10, 6))
plt.plot([1, 2, 3], [1, 4, 9])
plt.title('My Chart')
plt.savefig('/tmp/chart.png', dpi=150, bbox_inches='tight')
```

### PIL/Pillow (drawing, image manipulation)
```python
from PIL import Image, ImageDraw, ImageFont
img = Image.new('RGB', (400, 300), 'white')
draw = ImageDraw.Draw(img)
draw.rectangle([50, 50, 350, 250], outline='black', width=2)
img.save('/tmp/drawing.png')
```

## CRITICAL: Displaying Images to User

After generating an image, **always output it as inline base64 markdown** so it renders in chat:

```python
import base64
with open('/tmp/output.png', 'rb') as f:
    b64 = base64.b64encode(f.read()).decode()
print(f'![image](data:image/png;base64,{b64})')
```

**Do this in the same exec call as the image generation.** The markdown image tag will render directly in the chat.

## Guidelines
- Always use `matplotlib.use('Agg')` before importing pyplot
- NEVER use turtle, tkinter, pygame, or any GUI library
- Save images to /tmp/ directory
- Always output base64 inline — don't just report the file path
- For charts: use clear labels, titles, and legends
- Install missing packages with pip if needed: `pip install matplotlib pillow`
