FROM python:3.12-bookworm
# A generic numerical environment; domain nodes are authored by the agent.
RUN pip install --no-cache-dir numpy==2.2.4 scipy==1.15.2 pandas==2.2.3 scikit-learn==1.6.1 joblib==1.4.2
ENV OPENBLAS_NUM_THREADS=1 OMP_NUM_THREADS=1 MKL_NUM_THREADS=1
