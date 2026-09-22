import argparse


def run():
    parser = argparse.ArgumentParser(prog="hello-cli")
    parser.add_argument("--name", default="world")
    args = parser.parse_args()
    print("hello, {}!".format(args.name))


if __name__ == "__main__":
    run()
