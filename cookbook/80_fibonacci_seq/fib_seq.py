import click

DEFAULT_MAX_NUM = 50 

@click.command()
@click.option('--n', required=True, type=int, default=DEFAULT_MAX_NUM, help='Calculate Fibonacci numbers less than N')
def fibonacci(n: int = DEFAULT_MAX_NUM):
    """ Calculate and print Fibonacci numbers less than N """
    
    def generate_fibonacci(limit):
        fib_sequence = []
        a, b = 0, 1
        while a < limit:
            fib_sequence.append(a)
            a, b = b, a + b
        return fib_sequence

    if n <= 0:
        click.echo("Please enter a positive integer greater than 0.")
        return

    fib_numbers = generate_fibonacci(n)
    click.echo(f"Fibonacci numbers less than {n}:")
    for num in fib_numbers:
        click.echo(num)

if __name__ == '__main__':
    fibonacci()