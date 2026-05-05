# Harmony renderer

This project provides a structured way to create messages that get rendered into a set of tokens using our harmony prompt format.

The majority of the code is written in Rust inside `src/` and `src/py_module.rs` defines using pyo3 what functions are exposed to the python version of the library with a wrapper in `python/openai_harmony/__init__.py`.

You can build the Rust code along with the Python wrapper using `maturin develop --release`. This will also locally install the library in the venv so you can use it.

To test the library run `pytest`. That's the only way you can test it.
