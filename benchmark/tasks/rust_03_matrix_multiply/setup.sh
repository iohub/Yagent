#!/bin/bash
set -e
REPO="/tmp/repos/rust_03_matrix_multiply"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"
cargo init --lib --name matrix_multiply 2>/dev/null
cat >> Cargo.toml << 'TOML'
rayon = "1.10"
rand = "0.8"
TOML

mkdir -p tests
cat > tests/matrix_test.rs << 'TESTEOF'
use matrix_multiply::Matrix;

fn make_identity(n: usize) -> Matrix {
    let mut m = Matrix::new(n, n);
    for i in 0..n { m.set(i, i, 1.0); }
    m
}

#[test]
fn test_multiply_identity() {
    let a = Matrix::from_vec(2, 2, vec![1.0, 2.0, 3.0, 4.0]);
    let id = make_identity(2);
    let result = a.multiply(&id);
    assert_eq!(result.get(0, 0), 1.0);
    assert_eq!(result.get(0, 1), 2.0);
    assert_eq!(result.get(1, 0), 3.0);
    assert_eq!(result.get(1, 1), 4.0);
}

#[test]
fn test_multiply_basic() {
    let a = Matrix::from_vec(2, 3, vec![1.0, 2.0, 3.0, 4.0, 5.0, 6.0]);
    let b = Matrix::from_vec(3, 2, vec![7.0, 8.0, 9.0, 10.0, 11.0, 12.0]);
    let result = a.multiply(&b);
    assert_eq!(result.rows(), 2);
    assert_eq!(result.cols(), 2);
    assert_eq!(result.get(0, 0), 58.0);
    assert_eq!(result.get(0, 1), 64.0);
    assert_eq!(result.get(1, 0), 139.0);
    assert_eq!(result.get(1, 1), 154.0);
}

#[test]
fn test_multiply_parallel() {
    let a = Matrix::from_vec(3, 3, (1..=9).map(|x| x as f64).collect());
    let b = Matrix::from_vec(3, 3, (1..=9).map(|x| x as f64).collect());
    let seq = a.multiply_seq(&b);
    let par = a.multiply(&b);
    for i in 0..3 {
        for j in 0..3 {
            assert!((seq.get(i, j) - par.get(i, j)).abs() < 0.001);
        }
    }
}

#[test]
fn test_large_matrix() {
    let n = 128;
    let a = Matrix::new(n, n);
    let mut a = a;
    for i in 0..n { a.set(i, i, 2.0); }
    let id = make_identity(n);
    let result = a.multiply(&id);
    for i in 0..n {
        assert_eq!(result.get(i, i), 2.0);
    }
}

#[test]
fn test_add() {
    let a = Matrix::from_vec(2, 2, vec![1.0, 2.0, 3.0, 4.0]);
    let b = Matrix::from_vec(2, 2, vec![5.0, 6.0, 7.0, 8.0]);
    let result = a.add(&b);
    assert_eq!(result.get(0, 0), 6.0);
    assert_eq!(result.get(1, 1), 12.0);
}

#[test]
fn test_transpose() {
    let a = Matrix::from_vec(2, 3, vec![1.0, 2.0, 3.0, 4.0, 5.0, 6.0]);
    let t = a.transpose();
    assert_eq!(t.rows(), 3);
    assert_eq!(t.cols(), 2);
    assert_eq!(t.get(0, 0), 1.0);
    assert_eq!(t.get(0, 1), 4.0);
}
TESTEOF

cat > src/lib.rs << 'LIBEOF'
pub struct Matrix { /* implement */ }
impl Matrix {
    pub fn new(rows: usize, cols: usize) -> Self { todo!() }
    pub fn from_vec(rows: usize, cols: usize, data: Vec<f64>) -> Self { todo!() }
    pub fn rows(&self) -> usize { todo!() }
    pub fn cols(&self) -> usize { todo!() }
    pub fn get(&self, row: usize, col: usize) -> f64 { todo!() }
    pub fn set(&mut self, row: usize, col: usize, val: f64) { todo!() }
    pub fn multiply(&self, other: &Matrix) -> Matrix { todo!() }
    pub fn multiply_seq(&self, other: &Matrix) -> Matrix { todo!() }
    pub fn add(&self, other: &Matrix) -> Matrix { todo!() }
    pub fn transpose(&self) -> Matrix { todo!() }
}
LIBEOF

echo "rust_03_matrix_multiply setup done"
