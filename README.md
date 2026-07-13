# MolMesh

Минимальный локальный поиск молекул ChEMBL, похожих на гефитиниб (`CHEMBL939`).

Скрипт находит `CHEMBL939` в TSV и использует его `canonical_smiles` как эталон. Затем во втором потоковом проходе по файлу строит Morgan fingerprints (`radius=2`, `fpSize=2048`) и ранжирует остальные валидные SMILES по Tanimoto similarity. Невалидные SMILES и сам `CHEMBL939` пропускаются. В памяти остаются только 20 лучших результатов (или значение `--top`).

## Установка

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

RDKit также можно установить через conda-forge:

```bash
conda install -c conda-forge rdkit
```

## Запуск

```bash
python molmesh.py chembl_37_chemreps.txt -o gefitinib_similarities.csv
```

По умолчанию создаётся CSV с колонками `rank,chembl_id,canonical_smiles,similarity` и выводится тот же top-20 в терминал. Для другого размера выборки:

```bash
python molmesh.py chembl_37_chemreps.txt --top 50 -o top_50.csv
```

Во время поиска статус выводится в `stderr` каждые 100 000 строк: количество обработанных строк, текущая и средняя скорость, прошедшее время и число пропущенных невалидных SMILES. Период можно изменить или отключить:

```bash
python molmesh.py chembl_37_chemreps.txt --progress-every 500000
python molmesh.py chembl_37_chemreps.txt --progress-every 0
```

## Быстрый тест на части базы

Опция `--max-rows` ограничивает второй проход первыми `N` строками TSV. Сам `CHEMBL939` перед этим всё равно находится в отдельном потоковом проходе, поэтому эталон остаётся тем же. Получившийся CSV — это top‑20 только по обработанной части, а не по полной базе.

```bash
python molmesh.py chembl_37_chemreps.txt --max-rows 10000 -o test_results.csv
```

## Изображения структур

Передайте каталог в `--images-dir`, чтобы создать `CHEMBL939_gefitinib.png` с гефитинибом и `top_candidates.png` с сеткой top‑кандидатов. На изображениях кандидатов указаны ранг, ChEMBL ID и Tanimoto similarity.

```bash
python molmesh.py chembl_37_chemreps.txt --images-dir structures
```

Опция `--image-columns` задаёт число структур в строке сетки (по умолчанию `4`).
