package main

import (
	"fmt"
	"math/rand"
	"time"
)

// --- FUNÇÃO MinhaFuncaoLenta (NÃO USE PARA PROFILING DE CPU) ---
// Esta função passa a maior parte do tempo esperando, não trabalhando.
func MinhaFuncaoLenta() {
	time.Sleep(10 * time.Millisecond)
	parteLenta()
}

func parteLenta() {
	time.Sleep(20 * time.Millisecond)
}

// --- FUNÇÃO MinhaFuncaoCPUIntensiva (IDEAL PARA PROFILING DE CPU) ---
// Esta função executa um cálculo pesado para consumir ciclos de CPU.
// A variável 'result' no nível do pacote impede o compilador de otimizar o loop.
var result float64

func MinhaFuncaoCPUIntensiva() {
	var sum float64
	// Loop para simular trabalho computacional
	for i := 0; i < 2000; i++ {
		sum += parteCPUIntensiva()
	}
	result = sum
}

// Esta será nosso gargalo identificável.
func parteCPUIntensiva() float64 {
	var partialSum float64
	for i := 0; i < 5000; i++ {
		partialSum += rand.Float64() * rand.Float64()
	}
	return partialSum
}

func main() {
	// A função main não é relevante para o benchmark, mas precisa existir.
	fmt.Println("Executando main...")
	MinhaFuncaoCPUIntensiva()
	fmt.Println("Concluído.")
}
