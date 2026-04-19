---
name: data-analysis
description: Analyze data, process CSV/JSON files, compute statistics, and create data visualizations. Use when the user asks about data processing, statistics, or analysis.
metadata:
  fastclaw:
    always: false
---

# Data Analysis Skill

Analyze and process data using Python in the sandbox.

## Common Libraries
- **pandas**: DataFrames, CSV/JSON/Excel processing
- **numpy**: Numerical computing
- **matplotlib**: Visualization (use Agg backend)

Install if needed: `pip install pandas numpy matplotlib`

## Common Tasks

### Read and analyze CSV
```python
import pandas as pd
df = pd.read_csv('data.csv')
print(df.describe())
print(f"\nShape: {df.shape}")
print(f"\nColumns: {list(df.columns)}")
print(f"\nFirst 5 rows:\n{df.head()}")
```

### Create visualization from data
```python
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import pandas as pd
import base64

df = pd.read_csv('data.csv')
df.plot(kind='bar', x='category', y='value', figsize=(10, 6))
plt.title('Data Overview')
plt.tight_layout()
plt.savefig('/tmp/chart.png', dpi=150)

with open('/tmp/chart.png', 'rb') as f:
    print(f'![chart](data:image/png;base64,{base64.b64encode(f.read()).decode()})')
```

### JSON processing
```python
import json
with open('data.json') as f:
    data = json.load(f)
# Process and analyze...
```

## Guidelines
- Always execute the analysis — don't just show code
- Show key statistics: shape, dtypes, describe(), null counts
- For large datasets, show head/tail and summary stats
- Generate charts when it helps explain the data
- Use base64 inline images for any visualizations
